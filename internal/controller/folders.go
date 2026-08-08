package controller

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/cards"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/leaf"
	syncthingconfig "github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/uicontrol"
)

func reconcileManagedFolders(configured []syncthingconfig.ConfiguredFolder, inventory []cards.Card) ([]uicontrol.FolderStatus, []uicontrol.Issue) {
	bindings := make(map[string][]cards.Card)
	for _, card := range inventory {
		if card.Identity.ID == "" {
			continue
		}
		for _, kind := range []string{"saves", "states"} {
			folderID, _, err := cards.BindingNames(card.Identity.ID, kind)
			if err == nil {
				bindings[folderID] = append(bindings[folderID], card)
			}
		}
	}

	rows := make([]uicontrol.FolderStatus, 0, len(configured))
	issues := []uicontrol.Issue{}
	for _, folder := range configured {
		row := uicontrol.FolderStatus{
			ID: folder.ID, Label: folder.Label, Kind: folder.Kind, Path: folder.Path,
			Type: folder.Type, State: "paused", Paused: true,
			PauseReasons: []string{"first-sync"}, Versioning: folder.VersioningType,
			Issues: []uicontrol.Issue{},
		}
		if row.Label == "" {
			row.Label = "Leaf " + folderKindLabel(folder.Kind)
		}
		if row.Versioning == "" {
			row.Versioning = "none"
		}
		candidates := bindings[folder.ID]
		if len(candidates) != 1 {
			code := "unknown-card-binding"
			message := "No enrolled physical card matches this managed folder"
			if len(candidates) > 1 {
				code = "duplicate-card-id"
				message = "Multiple mounted cards match this managed folder identity"
			}
			addFolderIssue(&row, &issues, code, message)
			row.State = "error"
			rows = append(rows, row)
			continue
		}

		card := candidates[0]
		row.CardID = card.Identity.ID
		row.Label += " — " + sourceLabel(card.Source)
		expectedID, expectedMarker, _ := cards.BindingNames(card.Identity.ID, folder.Kind)
		expectedPath := managedContentPath(card.Source, folder.Kind)
		storageReason := "storage:" + card.Identity.ID
		if expectedID != folder.ID || expectedPath == "" || filepath.Clean(folder.Path) != filepath.Clean(expectedPath) {
			addFolderIssue(&row, &issues, "unsafe-folder-path", "The managed folder path does not match its physical card and PATH-2 content kind")
		}
		if card.State != cards.StateEnrolled || !card.Present || card.DuplicateID {
			row.PauseReasons = append(row.PauseReasons, storageReason)
			addFolderIssue(&row, &issues, "card-unavailable", "The enrolled physical card is absent, invalid, or duplicated")
		}
		receiveCapable := folder.Type != "sendonly"
		if folder.Type != "sendonly" && folder.Type != "sendreceive" && folder.Type != "receiveonly" {
			addFolderIssue(&row, &issues, "unsupported-folder-type", "The managed folder type is not supported by Leaf v1")
		}
		if receiveCapable && !card.Writable {
			row.PauseReasons = appendUnique(row.PauseReasons, storageReason)
			addFolderIssue(&row, &issues, "card-read-only", "A receive-capable folder requires a writable card")
		}
		if folder.MarkerName != expectedMarker {
			addFolderIssue(&row, &issues, "unsafe-folder-marker", "The folder does not use its binding-specific Leaf marker")
		}
		if expectedPath != "" && filepath.Clean(folder.Path) == filepath.Clean(expectedPath) && card.Present {
			switch err := cards.ValidateManagedMarker(expectedPath, expectedMarker); {
			case err == nil:
			case errors.Is(err, cards.ErrForeignMarker):
				addFolderIssue(&row, &issues, "foreign-folder-manager", "A default .stfolder shows that another Syncthing manages this directory")
			case errors.Is(err, cards.ErrMarkerMissing):
				addFolderIssue(&row, &issues, "marker-missing", "The binding-specific Leaf folder marker is missing")
			default:
				addFolderIssue(&row, &issues, "unsafe-folder-marker", "The folder root or marker is unsafe")
			}
		}
		if receiveCapable {
			expectedVersionPath := filepath.Join(card.Source.UserdataPath, leaf.AppStateName, "versions", folder.Kind)
			if folder.VersioningType != "simple" || filepath.Clean(folder.VersioningFSPath) != filepath.Clean(expectedVersionPath) || folder.VersioningFSType != "basic" {
				addFolderIssue(&row, &issues, "unsafe-versioning", "Receive-capable versioning must use the explicit same-card Leaf path and filesystem type")
			}
		}
		if len(row.Issues) > 0 {
			row.State = "error"
		}
		rows = append(rows, row)
	}
	return rows, issues
}

func addFolderIssue(row *uicontrol.FolderStatus, all *[]uicontrol.Issue, code, message string) {
	issue := uicontrol.Issue{Code: code, Message: message, Scope: "folder", SubjectID: row.ID}
	row.Issues = append(row.Issues, issue)
	*all = append(*all, issue)
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func managedContentPath(source leaf.Source, kind string) string {
	switch kind {
	case "saves":
		return source.SavesPath
	case "states":
		return source.StatesPath
	default:
		return ""
	}
}

func folderKindLabel(kind string) string {
	switch kind {
	case "saves":
		return "Saves"
	case "states":
		return "States"
	default:
		return fmt.Sprintf("Folder (%s)", kind)
	}
}
