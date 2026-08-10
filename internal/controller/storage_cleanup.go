package controller

import (
	"errors"
	"path/filepath"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/cards"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/leaf"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/uicontrol"
)

func cleanupStorageRow(inventory []cards.Card, status uicontrol.Status, controls map[string]folderControlRecord, cardSuffix, category, kind, name string, bytes int64) error {
	storage, err := storageInventory(inventory)
	if err != nil {
		return err
	}
	matches := 0
	for _, row := range storage.Inventory {
		if row.CardSuffix == cardSuffix && row.Category == category && row.Kind == kind &&
			row.Name == name && row.Bytes == bytes {
			matches++
		}
	}
	if matches != 1 {
		return errors.New("retained storage changed after confirmation")
	}
	var card cards.Card
	cardMatches := 0
	for _, candidate := range inventory {
		if candidate.Identity.ID != "" && identitySuffix(candidate.Identity.ID) == cardSuffix {
			card = candidate
			cardMatches++
		}
	}
	if cardMatches != 1 || !usableEnrolledCard(card) {
		return errors.New("retained storage card is unavailable or ambiguous")
	}
	for folderID, record := range controls {
		if record.CardID != card.Identity.ID || record.Kind != kind {
			continue
		}
		if category == "snapshot" && record.FirstSync {
			return errors.New("first-sync protection still uses this card's snapshots")
		}
		if category == "versions" {
			folder, found := findFolder(status, folderID)
			if !found || !folder.Paused {
				return errors.New("pause the managed folder before cleaning active version history")
			}
		}
	}
	stateRoot := filepath.Join(card.Source.UserdataPath, leaf.AppStateName)
	var target string
	switch category {
	case "snapshot":
		if filepath.Base(name) != name || name == "." || name == ".." {
			return errors.New("snapshot cleanup name is unsafe")
		}
		target = filepath.Join(stateRoot, "snapshots", kind, name)
	case "versions":
		target = filepath.Join(stateRoot, "versions", kind)
	default:
		return errors.New("storage cleanup category is unsupported")
	}
	if err := resetPathWithin(stateRoot, target); err != nil {
		return err
	}
	if err := removeDeclaredPath(target); err != nil {
		return err
	}
	return syncStateFilesystem(card.Source.Root)
}
