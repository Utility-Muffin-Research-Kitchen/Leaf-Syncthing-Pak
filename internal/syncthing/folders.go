package syncthing

import (
	"encoding/xml"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var managedFolderIDPattern = regexp.MustCompile(`^leaf-(saves|states)-[0-9a-f]{16}$`)

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
}

// ReadManagedFolders reads only Leaf-owned folder IDs. B1 never creates a
// folder; it recognizes pre-existing bindings so they can be paused before
// upstream starts and reported safely until B3 owns onboarding.
func ReadManagedFolders(configDir string) ([]ConfiguredFolder, error) {
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
	for _, folder := range document.Folders {
		if !strings.HasPrefix(folder.ID, "leaf-saves-") && !strings.HasPrefix(folder.ID, "leaf-states-") {
			continue
		}
		match := managedFolderIDPattern.FindStringSubmatch(folder.ID)
		if match == nil || seen[folder.ID] || strings.TrimSpace(folder.Path) == "" {
			return nil, fmt.Errorf("read managed folders: invalid or duplicate Leaf folder %q", folder.ID)
		}
		seen[folder.ID] = true
		folderType := folder.Type
		if folderType == "" {
			folderType = "sendreceive"
		}
		markerName := folder.MarkerName
		if markerName == "" {
			markerName = ".stfolder"
		}
		configured := ConfiguredFolder{
			ID: folder.ID, Label: folder.Label, Kind: match[1], Path: folder.Path,
			Type: folderType, MarkerName: markerName,
			VersioningType: folder.Versioning.Type, VersioningFSPath: folder.Versioning.FSPath,
			VersioningFSType: folder.Versioning.FSType,
		}
		if folder.Paused != nil {
			configured.Paused = *folder.Paused
		}
		folders = append(folders, configured)
		if len(folders) > maxManagedFolders {
			return nil, errors.New("read managed folders: too many Leaf folders")
		}
	}
	return folders, nil
}
