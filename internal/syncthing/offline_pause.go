package syncthing

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type PauseEditResult struct {
	Changed bool
}

// ApplyOfflinePauseSet is the only editor for a committed upstream config.
// It changes only the <paused> scalar of explicitly managed folder IDs, then
// promotes through config.xml.tmp/config.xml.bak with a filesystem-wide flush.
func ApplyOfflinePauseSet(configDir string, desired map[string]bool, syncFilesystem SyncFilesystemFunc) (PauseEditResult, error) {
	if syncFilesystem == nil {
		syncFilesystem = syncFilesystemAt
	}
	if err := requireRealDirectory(configDir); err != nil {
		return PauseEditResult{}, err
	}
	configPath := filepath.Join(configDir, "config.xml")
	contents, err := readSafeConfig(configPath)
	if err != nil {
		return PauseEditResult{}, err
	}
	rewritten, changed, err := replaceManagedFolderPauses(contents, desired)
	if err != nil {
		return PauseEditResult{}, err
	}
	if !changed {
		if err := ValidateXML(configPath); err != nil {
			return PauseEditResult{}, err
		}
		if err := verifyPauseSet(configPath, desired); err != nil {
			return PauseEditResult{}, err
		}
		return PauseEditResult{}, nil
	}

	temporaryPath := filepath.Join(configDir, "config.xml.tmp")
	backupPath := filepath.Join(configDir, "config.xml.bak")
	if _, err := os.Lstat(temporaryPath); err == nil {
		return PauseEditResult{}, errors.New("offline pause edit: stale config.xml.tmp requires recovery")
	} else if !os.IsNotExist(err) {
		return PauseEditResult{}, err
	}
	backupExists := false
	if info, err := os.Lstat(backupPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return PauseEditResult{}, errors.New("offline pause edit: unsafe config.xml.bak")
		}
		backupExists = true
	} else if !os.IsNotExist(err) {
		return PauseEditResult{}, err
	}

	temporary, err := os.OpenFile(temporaryPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return PauseEditResult{}, fmt.Errorf("create offline config temporary: %w", err)
	}
	if _, err := temporary.Write(rewritten); err != nil {
		_ = temporary.Close()
		return PauseEditResult{}, fmt.Errorf("write offline config temporary: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return PauseEditResult{}, fmt.Errorf("flush offline config temporary: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return PauseEditResult{}, fmt.Errorf("close offline config temporary: %w", err)
	}
	if err := ValidateXML(temporaryPath); err != nil {
		return PauseEditResult{}, fmt.Errorf("validate offline config temporary: %w", err)
	}
	if err := verifyPauseSet(temporaryPath, desired); err != nil {
		return PauseEditResult{}, err
	}

	if backupExists {
		if err := os.Remove(backupPath); err != nil {
			return PauseEditResult{}, fmt.Errorf("remove previous config backup: %w", err)
		}
	}
	if err := os.Rename(configPath, backupPath); err != nil {
		return PauseEditResult{}, fmt.Errorf("move known-good config to backup: %w", err)
	}
	if err := os.Rename(temporaryPath, configPath); err != nil {
		return PauseEditResult{}, fmt.Errorf("promote offline config temporary: %w", err)
	}
	if err := syncFilesystem(configDir); err != nil {
		return PauseEditResult{}, fmt.Errorf("flush offline config transaction: %w", err)
	}
	if err := ValidateXML(configPath); err != nil {
		return PauseEditResult{}, restoreBackup(configPath, temporaryPath, backupPath, configDir, syncFilesystem, err)
	}
	if err := verifyPauseSet(configPath, desired); err != nil {
		return PauseEditResult{}, restoreBackup(configPath, temporaryPath, backupPath, configDir, syncFilesystem, err)
	}
	return PauseEditResult{Changed: true}, nil
}

func replaceManagedFolderPauses(contents []byte, desired map[string]bool) ([]byte, bool, error) {
	if len(desired) == 0 {
		return contents, false, nil
	}
	for id := range desired {
		if id == "" {
			return nil, false, errors.New("offline pause edit: managed folder id is empty")
		}
	}
	decoder := xml.NewDecoder(bytes.NewReader(contents))
	var output bytes.Buffer
	encoder := xml.NewEncoder(&output)
	stack := make([]string, 0, 8)
	found := make(map[string]bool, len(desired))
	activeID := ""
	activePausedSeen := false
	changed := false

	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, false, fmt.Errorf("offline pause edit: decode config: %w", err)
		}
		switch typed := token.(type) {
		case xml.StartElement:
			if len(stack) == 1 && stack[0] == "configuration" && typed.Name.Local == "folder" {
				id := attributeValue(typed.Attr, "id")
				if _, managed := desired[id]; managed {
					if found[id] {
						return nil, false, fmt.Errorf("offline pause edit: duplicate managed folder %q", id)
					}
					found[id] = true
					activeID = id
					activePausedSeen = false
				}
			}
			if activeID != "" && len(stack) == 2 && stack[1] == "folder" && typed.Name.Local == "paused" {
				if activePausedSeen {
					return nil, false, fmt.Errorf("offline pause edit: folder %q has duplicate paused fields", activeID)
				}
				activePausedSeen = true
				if err := encoder.EncodeToken(typed); err != nil {
					return nil, false, err
				}
				end, existing, err := consumeScalarText(decoder, typed)
				if err != nil {
					return nil, false, err
				}
				value := strconv.FormatBool(desired[activeID])
				if strings.TrimSpace(existing) != value {
					changed = true
				}
				if err := encoder.EncodeToken(xml.CharData(value)); err != nil {
					return nil, false, err
				}
				if err := encoder.EncodeToken(end); err != nil {
					return nil, false, err
				}
				continue
			}
			stack = append(stack, typed.Name.Local)
			if err := encoder.EncodeToken(typed); err != nil {
				return nil, false, err
			}
		case xml.EndElement:
			if len(stack) == 0 || stack[len(stack)-1] != typed.Name.Local {
				return nil, false, errors.New("offline pause edit: unbalanced XML stack")
			}
			if activeID != "" && len(stack) == 2 && stack[1] == "folder" {
				if !activePausedSeen {
					start := xml.StartElement{Name: xml.Name{Local: "paused"}}
					if err := encoder.EncodeToken(start); err != nil {
						return nil, false, err
					}
					if err := encoder.EncodeToken(xml.CharData(strconv.FormatBool(desired[activeID]))); err != nil {
						return nil, false, err
					}
					if err := encoder.EncodeToken(start.End()); err != nil {
						return nil, false, err
					}
					changed = true
				}
				activeID = ""
				activePausedSeen = false
			}
			stack = stack[:len(stack)-1]
			if err := encoder.EncodeToken(typed); err != nil {
				return nil, false, err
			}
		default:
			if err := encoder.EncodeToken(token); err != nil {
				return nil, false, err
			}
		}
	}
	if len(stack) != 0 {
		return nil, false, errors.New("offline pause edit: incomplete XML")
	}
	for id := range desired {
		if !found[id] {
			return nil, false, fmt.Errorf("offline pause edit: managed folder %q is absent", id)
		}
	}
	if err := encoder.Flush(); err != nil {
		return nil, false, err
	}
	if !changed {
		return contents, false, nil
	}
	return output.Bytes(), true, nil
}

func consumeScalarText(decoder *xml.Decoder, start xml.StartElement) (xml.EndElement, string, error) {
	var text strings.Builder
	for {
		token, err := decoder.Token()
		if err != nil {
			return xml.EndElement{}, "", err
		}
		switch typed := token.(type) {
		case xml.CharData:
			text.Write([]byte(typed))
		case xml.StartElement:
			return xml.EndElement{}, "", fmt.Errorf("offline pause edit: scalar %s contains a nested element", start.Name.Local)
		case xml.EndElement:
			if typed.Name != start.Name {
				return xml.EndElement{}, "", errors.New("offline pause edit: scalar closing element mismatch")
			}
			return typed, text.String(), nil
		}
	}
}

func verifyPauseSet(path string, desired map[string]bool) error {
	if len(desired) == 0 {
		return nil
	}
	type folder struct {
		ID     string `xml:"id,attr"`
		Paused *bool  `xml:"paused"`
	}
	var configuration struct {
		XMLName xml.Name `xml:"configuration"`
		Folders []folder `xml:"folder"`
	}
	contents, err := readSafeConfig(path)
	if err != nil {
		return err
	}
	if err := xml.Unmarshal(contents, &configuration); err != nil {
		return err
	}
	found := make(map[string]bool, len(desired))
	for _, folder := range configuration.Folders {
		want, managed := desired[folder.ID]
		if !managed {
			continue
		}
		if found[folder.ID] || folder.Paused == nil || *folder.Paused != want {
			return fmt.Errorf("offline pause edit: folder %q did not round-trip", folder.ID)
		}
		found[folder.ID] = true
	}
	for id := range desired {
		if !found[id] {
			return fmt.Errorf("offline pause edit: folder %q missing after round-trip", id)
		}
	}
	return nil
}

func restoreBackup(configPath, temporaryPath, backupPath, configDir string, syncFilesystem SyncFilesystemFunc, cause error) error {
	_ = os.Remove(temporaryPath)
	if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("offline pause edit failed (%v) and bad config removal failed: %w", cause, err)
	}
	if err := os.Rename(backupPath, configPath); err != nil {
		return fmt.Errorf("offline pause edit failed (%v) and backup restore failed: %w", cause, err)
	}
	if err := syncFilesystem(configDir); err != nil {
		return fmt.Errorf("offline pause edit failed (%v) and restored config flush failed: %w", cause, err)
	}
	if err := ValidateXML(configPath); err != nil {
		return fmt.Errorf("offline pause edit failed (%v) and restored config is invalid: %w", cause, err)
	}
	return fmt.Errorf("offline pause edit rejected promoted config and restored backup: %w", cause)
}

func readSafeConfig(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > MaxConfigBytes {
		return nil, errors.New("offline pause edit: config is unsafe or oversized")
	}
	return os.ReadFile(path)
}

func attributeValue(attributes []xml.Attr, name string) string {
	for _, attribute := range attributes {
		if attribute.Name.Local == name {
			return attribute.Value
		}
	}
	return ""
}
