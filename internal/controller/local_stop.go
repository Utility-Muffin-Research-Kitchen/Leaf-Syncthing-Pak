package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/cards"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
)

func hasPendingFolderStop(records map[string]folderControlRecord) bool {
	for _, record := range records {
		if record.PendingStop {
			return true
		}
	}
	return false
}

func recoverPendingFolderStops(ctx context.Context, folders []syncthing.ConfiguredFolder, inventory []cards.Card, controls *folderControlStore, upstream b3FolderUpstream) ([]syncthing.ConfiguredFolder, error) {
	result := append([]syncthing.ConfiguredFolder(nil), folders...)
	for folderID, record := range controls.Snapshot() {
		if !record.PendingStop {
			continue
		}
		folder, index, configured := findConfiguredFolder(result, folderID)
		card, ok := cardForStop(record, inventory)
		if !ok || !usableEnrolledCard(card) {
			return nil, errors.New("pending local stop requires its enrolled writable card")
		}
		if !configured {
			folder = syncthing.ConfiguredFolder{
				ID: folderID, Kind: record.Kind, Path: managedContentPath(card.Source, record.Kind),
				MarkerName: record.MarkerName,
			}
		}
		if err := validateStopMarker(folder, card, configured); err != nil {
			return nil, err
		}
		if configured {
			if err := upstream.SetFolderPaused(ctx, folderID, true); err != nil {
				return nil, err
			}
		}
		if err := upstream.RemoveManagedFolder(ctx, folderID); err != nil {
			return nil, err
		}
		if err := removeStopMarker(folder, card); err != nil {
			return nil, err
		}
		if err := controls.Remove(folderID); err != nil {
			return nil, err
		}
		if configured {
			result = append(result[:index], result[index+1:]...)
		}
	}
	return result, nil
}

func cardForStop(record folderControlRecord, inventory []cards.Card) (cards.Card, bool) {
	var result cards.Card
	matches := 0
	for _, card := range inventory {
		if card.Identity.ID == record.CardID {
			result = card
			matches++
		}
	}
	return result, matches == 1
}

func validateStopMarker(folder syncthing.ConfiguredFolder, card cards.Card, required bool) error {
	expectedPath := managedContentPath(card.Source, folder.Kind)
	if folder.ID == "" || folder.Kind == "" || folder.MarkerName == "" ||
		folder.MarkerName == ".stfolder" || filepath.Base(folder.MarkerName) != folder.MarkerName ||
		expectedPath == "" || filepath.Clean(folder.Path) != filepath.Clean(expectedPath) {
		return errors.New("local stop binding does not match its physical card")
	}
	root, err := os.Lstat(expectedPath)
	if err != nil || root.Mode()&os.ModeSymlink != 0 || !root.IsDir() {
		return errors.New("local stop live tree is unavailable or unsafe")
	}
	marker, err := os.Lstat(filepath.Join(expectedPath, folder.MarkerName))
	if os.IsNotExist(err) && !required {
		return nil
	}
	if err != nil || marker.Mode()&os.ModeSymlink != 0 || !marker.IsDir() {
		return errors.New("local stop marker is unavailable or unsafe")
	}
	entries, err := os.ReadDir(filepath.Join(expectedPath, folder.MarkerName))
	if err != nil || len(entries) != 0 {
		return errors.New("local stop marker is not an empty Leaf directory")
	}
	return nil
}

func removeStopMarker(folder syncthing.ConfiguredFolder, card cards.Card) error {
	if err := validateStopMarker(folder, card, false); err != nil {
		return err
	}
	markerPath := filepath.Join(folder.Path, folder.MarkerName)
	if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncStateFilesystem(card.Source.Root)
}
