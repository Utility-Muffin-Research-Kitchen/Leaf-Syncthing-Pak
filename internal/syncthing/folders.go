package syncthing

import (
	"encoding/xml"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var legacyManagedFolderIDPattern = regexp.MustCompile(`^leaf-(saves|states)-[0-9a-f]{16}$`)

const maxManagedFolders = 16

type ConfiguredFolder struct {
	ID               string
	Label            string
	Kind             string
	Path             string
	Type             string
	MarkerName       string
	Paused           bool
	VersioningType   string
	VersioningFSPath string
	VersioningFSType string
	Devices          []string
}

// ValidFolderID bounds IDs that cross the controller and REST path boundary.
func ValidFolderID(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, character := range []byte(value) {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '.' || character == '_' ||
			character == ':' || character == '-' {
			continue
		}
		return false
	}
	return true
}

// ReadManagedFolders preserves the strict B3 discovery rule used to migrate
// pre-binding-registry configurations.
func ReadManagedFolders(configDir string) ([]ConfiguredFolder, error) {
	return readManagedFolders(configDir, nil)
}

// ReadManagedFoldersForBindings selects exactly the IDs registered by the
// controller. Network folder IDs do not need a Leaf-specific prefix.
func ReadManagedFoldersForBindings(configDir string, bindings map[string]string) ([]ConfiguredFolder, error) {
	if bindings == nil {
		bindings = map[string]string{}
	}
	if len(bindings) > maxManagedFolders {
		return nil, errors.New("read managed folders: too many registered bindings")
	}
	for folderID, kind := range bindings {
		if !ValidFolderID(folderID) || (kind != "saves" && kind != "states") {
			return nil, errors.New("read managed folders: invalid registered binding")
		}
	}
	return readManagedFolders(configDir, bindings)
}

func readManagedFolders(configDir string, bindings map[string]string) ([]ConfiguredFolder, error) {
	contents, err := readSafeConfig(filepath.Join(configDir, "config.xml"))
	if err != nil {
		return nil, err
	}
	var document struct {
		XMLName xml.Name `xml:"configuration"`
		Folders []struct {
			ID         string `xml:"id,attr"`
			Label      string `xml:"label,attr"`
			Path       string `xml:"path,attr"`
			Type       string `xml:"type,attr"`
			Paused     *bool  `xml:"paused"`
			MarkerName string `xml:"markerName"`
			Versioning struct {
				Type   string `xml:"type,attr"`
				FSPath string `xml:"fsPath"`
				FSType string `xml:"fsType"`
			} `xml:"versioning"`
			Devices []struct {
				ID string `xml:"id,attr"`
			} `xml:"device"`
		} `xml:"folder"`
	}
	if err := xml.Unmarshal(contents, &document); err != nil {
		return nil, fmt.Errorf("read managed folders: %w", err)
	}
	if document.XMLName.Local != "configuration" {
		return nil, errors.New("read managed folders: invalid config root")
	}
	folders := make([]ConfiguredFolder, 0, len(document.Folders))
	seen := make(map[string]bool)
	remaining := make(map[string]bool, len(bindings))
	for folderID := range bindings {
		remaining[folderID] = true
	}
	for _, folder := range document.Folders {
		kind := ""
		if bindings != nil {
			var managed bool
			kind, managed = bindings[folder.ID]
			if !managed {
				continue
			}
		} else {
			if !strings.HasPrefix(folder.ID, "leaf-saves-") && !strings.HasPrefix(folder.ID, "leaf-states-") {
				continue
			}
			match := legacyManagedFolderIDPattern.FindStringSubmatch(folder.ID)
			if match == nil {
				return nil, fmt.Errorf("read managed folders: invalid Leaf folder %q", folder.ID)
			}
			kind = match[1]
		}
		if !ValidFolderID(folder.ID) || seen[folder.ID] || strings.TrimSpace(folder.Path) == "" {
			return nil, fmt.Errorf("read managed folders: invalid or duplicate Leaf folder %q", folder.ID)
		}
		seen[folder.ID] = true
		delete(remaining, folder.ID)
		folderType := folder.Type
		if folderType == "" {
			folderType = "sendreceive"
		}
		markerName := folder.MarkerName
		if markerName == "" {
			markerName = ".stfolder"
		}
		configured := ConfiguredFolder{
			ID: folder.ID, Label: folder.Label, Kind: kind, Path: folder.Path,
			Type: folderType, MarkerName: markerName,
			VersioningType: folder.Versioning.Type, VersioningFSPath: folder.Versioning.FSPath,
			VersioningFSType: folder.Versioning.FSType,
		}
		deviceSeen := make(map[string]bool)
		for _, device := range folder.Devices {
			if device.ID == "" || deviceSeen[device.ID] {
				return nil, fmt.Errorf("read managed folders: invalid or duplicate device in %q", folder.ID)
			}
			deviceSeen[device.ID] = true
			configured.Devices = append(configured.Devices, device.ID)
		}
		if folder.Paused != nil {
			configured.Paused = *folder.Paused
		}
		folders = append(folders, configured)
		if len(folders) > maxManagedFolders {
			return nil, errors.New("read managed folders: too many Leaf folders")
		}
	}
	if len(remaining) != 0 {
		return nil, errors.New("read managed folders: registered folder is missing from upstream config")
	}
	return folders, nil
}
