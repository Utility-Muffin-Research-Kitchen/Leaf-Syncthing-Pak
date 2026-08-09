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

func reconcileManagedFolders(configured []syncthingconfig.ConfiguredFolder, inventory []cards.Card, controlState map[string]folderControlRecord) ([]uicontrol.FolderStatus, []uicontrol.Issue) {
	cardsByID := make(map[string][]cards.Card)
	for _, card := range inventory {
		if card.Identity.ID == "" {
			continue
		}
		cardsByID[card.Identity.ID] = append(cardsByID[card.Identity.ID], card)
	}

	rows := make([]uicontrol.FolderStatus, 0, len(configured))
	issues := []uicontrol.Issue{}
	for _, folder := range configured {
		control, registered := controlState[folder.ID]
		reasons := []string{}
		if control.Manual {
			reasons = append(reasons, "manual")
		}
		if control.FirstSync {
			reasons = append(reasons, "first-sync")
		}
		row := uicontrol.FolderStatus{
			ID: folder.ID, Label: folder.Label, Kind: folder.Kind, Path: folder.Path,
			Type: folder.Type, State: "idle",
			PauseReasons: reasons, PendingRescan: control.PendingRescan, Versioning: folder.VersioningType,
			Issues: []uicontrol.Issue{}, PeerCount: remotePeerCount(folder.Devices),
		}
		if row.Label == "" {
			row.Label = "Leaf " + folderKindLabel(folder.Kind)
		}
		if row.Versioning == "" {
			row.Versioning = "none"
		}
		candidates := []cards.Card{}
		expectedMarker := ""
		if registered && completeFolderBinding(control) {
			row.CardID = control.CardID
			candidates = cardsByID[control.CardID]
			expectedMarker = control.MarkerName
			if folder.Kind != control.Kind {
				addFolderIssue(&row, &issues, "unsafe-folder-kind", "The managed folder kind does not match its registered Leaf binding")
			}
		} else {
			addFolderIssue(&row, &issues, "unregistered-folder", "The managed folder has no durable Leaf card binding")
			row.PauseReasons = appendUnique(row.PauseReasons, "health")
			row.Paused = true
			row.State = "error"
			rows = append(rows, row)
			continue
		}
		if len(candidates) != 1 {
			code := "unknown-card-binding"
			message := "No enrolled physical card matches this managed folder"
			if len(candidates) > 1 {
				code = "duplicate-card-id"
				message = "Multiple mounted cards match this managed folder identity"
			}
			addFolderIssue(&row, &issues, code, message)
			row.PauseReasons = appendUnique(row.PauseReasons, "health")
			row.Paused = true
			row.State = "error"
			rows = append(rows, row)
			continue
		}

		card := candidates[0]
		row.CardID = card.Identity.ID
		row.Label += " — " + sourceLabel(card.Source)
		expectedPath := managedContentPath(card.Source, folder.Kind)
		storageReason := "storage:" + card.Identity.ID
		if expectedPath == "" || filepath.Clean(folder.Path) != filepath.Clean(expectedPath) {
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
			row.PauseReasons = appendUnique(row.PauseReasons, "health")
			row.State = "error"
		}
		row.Paused = len(row.PauseReasons) > 0
		if row.Paused && len(row.Issues) == 0 {
			row.State = "paused"
		}
		rows = append(rows, row)
	}
	return rows, issues
}

func requiredOfflinePauseSet(configured []syncthingconfig.ConfiguredFolder, inventory []cards.Card, controlState map[string]folderControlRecord) map[string]bool {
	rows, _ := reconcileManagedFolders(configured, inventory, controlState)
	result := make(map[string]bool, len(rows))
	for _, row := range rows {
		result[row.ID] = row.Paused
	}
	return result
}

func remotePeerCount(devices []string) int {
	count := len(devices) - 1
	if count < 0 {
		return 0
	}
	return count
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
