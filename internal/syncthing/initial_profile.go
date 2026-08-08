package syncthing

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type scalarReplacement struct {
	path  string
	name  string
	value string
}

// ApplyInitialProfile changes only the controller-owned fields of a freshly
// generated, still-uncommitted config. Unknown elements, attributes, comments,
// and directives pass through the token stream unchanged.
func ApplyInitialProfile(configPath, guiSocket string) error {
	if !filepath.IsAbs(guiSocket) || filepath.Base(guiSocket) != "syncthing-gui.sock" {
		return errors.New("initial profile: GUI socket must be an absolute syncthing-gui.sock path")
	}
	info, err := os.Lstat(configPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > MaxConfigBytes {
		return errors.New("initial profile: config is unsafe or oversized")
	}
	contents, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	replacements := []scalarReplacement{
		{path: "configuration/gui/address", name: "address", value: guiSocket},
		{path: "configuration/gui/unixSocketPermissions", name: "unixSocketPermissions", value: "0600"},
		{path: "configuration/options/globalAnnounceEnabled", name: "globalAnnounceEnabled", value: "false"},
		{path: "configuration/options/localAnnounceEnabled", name: "localAnnounceEnabled", value: "true"},
		{path: "configuration/options/relaysEnabled", name: "relaysEnabled", value: "false"},
		{path: "configuration/options/natEnabled", name: "natEnabled", value: "false"},
		{path: "configuration/options/urAccepted", name: "urAccepted", value: "-1"},
		{path: "configuration/options/autoUpgradeIntervalH", name: "autoUpgradeIntervalH", value: "0"},
		{path: "configuration/options/startBrowser", name: "startBrowser", value: "false"},
		{path: "configuration/options/crashReportingEnabled", name: "crashReportingEnabled", value: "false"},
	}
	rewritten, err := replaceXMLScalars(contents, replacements)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(configPath, os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := file.Write(rewritten); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ValidateXML(configPath); err != nil {
		return fmt.Errorf("validate initial profile: %w", err)
	}
	return nil
}

func replaceXMLScalars(contents []byte, replacements []scalarReplacement) ([]byte, error) {
	byPath := make(map[string]scalarReplacement, len(replacements))
	for _, replacement := range replacements {
		if _, exists := byPath[replacement.path]; exists {
			return nil, fmt.Errorf("initial profile: duplicate replacement %s", replacement.path)
		}
		byPath[replacement.path] = replacement
	}
	seen := make(map[string]bool, len(replacements))
	decoder := xml.NewDecoder(bytes.NewReader(contents))
	var output bytes.Buffer
	encoder := xml.NewEncoder(&output)
	stack := make([]string, 0, 8)
	rootCount := 0

	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("initial profile: decode XML: %w", err)
		}
		switch typed := token.(type) {
		case xml.StartElement:
			if len(stack) == 0 {
				rootCount++
				if typed.Name.Local != "configuration" {
					return nil, fmt.Errorf("initial profile: unexpected root %q", typed.Name.Local)
				}
			}
			path := joinedXMLPath(stack, typed.Name.Local)
			if replacement, ok := byPath[path]; ok {
				if seen[path] {
					return nil, fmt.Errorf("initial profile: duplicate scalar %s", path)
				}
				seen[path] = true
				if err := encoder.EncodeToken(typed); err != nil {
					return nil, err
				}
				end, err := consumeScalar(decoder, typed)
				if err != nil {
					return nil, err
				}
				if err := encoder.EncodeToken(xml.CharData(replacement.value)); err != nil {
					return nil, err
				}
				if err := encoder.EncodeToken(end); err != nil {
					return nil, err
				}
				continue
			}
			stack = append(stack, typed.Name.Local)
			if err := encoder.EncodeToken(typed); err != nil {
				return nil, err
			}
		case xml.EndElement:
			if len(stack) == 0 || stack[len(stack)-1] != typed.Name.Local {
				return nil, errors.New("initial profile: unbalanced XML stack")
			}
			parentPath := strings.Join(stack, "/")
			for _, replacement := range replacements {
				if seen[replacement.path] || filepath.Dir(replacement.path) != parentPath {
					continue
				}
				start := xml.StartElement{Name: xml.Name{Local: replacement.name}}
				if err := encoder.EncodeToken(start); err != nil {
					return nil, err
				}
				if err := encoder.EncodeToken(xml.CharData(replacement.value)); err != nil {
					return nil, err
				}
				if err := encoder.EncodeToken(start.End()); err != nil {
					return nil, err
				}
				seen[replacement.path] = true
			}
			stack = stack[:len(stack)-1]
			if err := encoder.EncodeToken(typed); err != nil {
				return nil, err
			}
		default:
			if err := encoder.EncodeToken(token); err != nil {
				return nil, err
			}
		}
	}
	if rootCount != 1 || len(stack) != 0 {
		return nil, errors.New("initial profile: config must contain one complete root")
	}
	for _, replacement := range replacements {
		if !seen[replacement.path] {
			return nil, fmt.Errorf("initial profile: missing parent for %s", replacement.path)
		}
	}
	if err := encoder.Flush(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func consumeScalar(decoder *xml.Decoder, start xml.StartElement) (xml.EndElement, error) {
	for {
		token, err := decoder.Token()
		if err != nil {
			return xml.EndElement{}, err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			return xml.EndElement{}, fmt.Errorf("initial profile: managed scalar %s contains a nested element", start.Name.Local)
		case xml.EndElement:
			if typed.Name != start.Name {
				return xml.EndElement{}, fmt.Errorf("initial profile: managed scalar %s closes as %s", start.Name.Local, typed.Name.Local)
			}
			return typed, nil
		}
	}
}

func joinedXMLPath(stack []string, name string) string {
	if len(stack) == 0 {
		return name
	}
	return strings.Join(stack, "/") + "/" + name
}
