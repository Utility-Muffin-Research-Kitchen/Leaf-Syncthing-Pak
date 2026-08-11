package controller

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/cards"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/leaf"
	syncthingconfig "github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
)

// relocateManagedFolders follows an enrolled physical card when PATH-2 gives
// that same card a different mountpoint after reboot. It never guesses by slot:
// the durable card-id must resolve once and the existing custom marker must be
// present at the new path before upstream configuration changes.
func relocateManagedFolders(ctx context.Context, folders []syncthingconfig.ConfiguredFolder, inventory []cards.Card, controls map[string]folderControlRecord, upstream b3FolderUpstream) ([]syncthingconfig.ConfiguredFolder, error) {
	result := append([]syncthingconfig.ConfiguredFolder(nil), folders...)
	relocated := make(map[string]bool)
	for index, folder := range result {
		binding, found := controls[folder.ID]
		if !found || !completeFolderBinding(binding) || binding.Kind != folder.Kind ||
			binding.MarkerName != folder.MarkerName || binding.PendingAdd ||
			binding.PendingMembership != "" || binding.PendingStop {
			continue
		}
		if folder.Type != "sendonly" && folder.Type != "sendreceive" && folder.Type != "receiveonly" {
			continue
		}
		card, unique := cardForConfiguredFolder(folder, inventory, controls)
		if !unique || !usableEnrolledCard(card) {
			continue
		}
		expectedPath := managedContentPath(card.Source, binding.Kind)
		if expectedPath == "" {
			continue
		}
		expectedVersions := ""
		locationChanged := filepath.Clean(folder.Path) != filepath.Clean(expectedPath)
		if folder.Type != "sendonly" {
			expectedVersions = filepath.Join(card.Source.UserdataPath, leaf.AppStateName, "versions", binding.Kind)
			locationChanged = locationChanged || folder.VersioningType != "simple" ||
				folder.VersioningFSType != "basic" ||
				filepath.Clean(folder.VersioningFSPath) != filepath.Clean(expectedVersions)
		}
		if !locationChanged {
			continue
		}
		if err := cards.ValidateManagedMarker(expectedPath, binding.MarkerName); err != nil {
			continue
		}
		if folder.Type != "sendonly" {
			if err := ensureSafeDirectoryChain(card.Source.Root, expectedVersions); err != nil {
				return nil, err
			}
		}
		if err := upstream.SetFolderPaused(ctx, folder.ID, true); err != nil {
			return nil, err
		}
		target := folder
		target.Path = expectedPath
		target.Paused = true
		if target.Type != "sendonly" {
			target.VersioningType = "simple"
			target.VersioningFSPath = expectedVersions
			target.VersioningFSType = "basic"
		}
		if err := upstream.RelocateManagedFolder(ctx, target); err != nil {
			return nil, err
		}
		result[index] = target
		relocated[target.ID] = true
	}
	if len(relocated) == 0 {
		return result, nil
	}
	pauseSet := requiredOfflinePauseSet(result, inventory, controls)
	for index := range result {
		if !relocated[result[index].ID] {
			continue
		}
		paused, found := pauseSet[result[index].ID]
		if !found {
			return nil, errors.New("relocated folder is missing from the safety result")
		}
		if err := upstream.SetFolderPaused(ctx, result[index].ID, paused); err != nil {
			return nil, err
		}
		result[index].Paused = paused
	}
	return result, nil
}
