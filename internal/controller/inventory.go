package controller

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/cards"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/leaf"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/uicontrol"
)

const maxStorageInventoryRows = 128

func storageInventory(inventory []cards.Card) (uicontrol.StorageStatus, error) {
	status := uicontrol.StorageStatus{Inventory: []uicontrol.SnapshotStatus{}}
	for _, card := range inventory {
		if card.Identity.ID == "" || !card.Present || card.State != cards.StateEnrolled || card.DuplicateID {
			continue
		}
		stateRoot := filepath.Join(card.Source.UserdataPath, leaf.AppStateName)
		if err := collectSnapshotRows(&status, filepath.Join(stateRoot, "snapshots"), identitySuffix(card.Identity.ID)); err != nil {
			return uicontrol.StorageStatus{}, err
		}
		if err := collectVersionRows(&status, filepath.Join(stateRoot, "versions"), identitySuffix(card.Identity.ID)); err != nil {
			return uicontrol.StorageStatus{}, err
		}
	}
	sort.Slice(status.Inventory, func(left, right int) bool {
		if status.Inventory[left].CardSuffix != status.Inventory[right].CardSuffix {
			return status.Inventory[left].CardSuffix < status.Inventory[right].CardSuffix
		}
		if status.Inventory[left].Category != status.Inventory[right].Category {
			return status.Inventory[left].Category < status.Inventory[right].Category
		}
		return status.Inventory[left].Name < status.Inventory[right].Name
	})
	return status, nil
}

func collectSnapshotRows(status *uicontrol.StorageStatus, root, cardSuffix string) error {
	kinds, exists, err := safeDirectoryEntries(root)
	if err != nil || !exists {
		return err
	}
	for _, kindEntry := range kinds {
		kindPath := filepath.Join(root, kindEntry.Name())
		if kindEntry.Type()&os.ModeSymlink != 0 || !kindEntry.IsDir() {
			return fmt.Errorf("snapshot inventory contains an unsafe kind entry %s", kindPath)
		}
		entries, _, err := safeDirectoryEntries(kindPath)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			path := filepath.Join(kindPath, entry.Name())
			bytes, err := safeTreeBytes(path)
			if err != nil {
				return err
			}
			if len(status.Inventory) >= maxStorageInventoryRows {
				return errors.New("snapshot inventory exceeds row limit")
			}
			status.Inventory = append(status.Inventory, uicontrol.SnapshotStatus{
				CardSuffix: cardSuffix, Category: "snapshot", Kind: inventoryKind(kindEntry.Name()),
				Name: boundedInventoryName(entry.Name()), Bytes: bytes,
			})
			status.SnapshotBytes += bytes
			status.SnapshotCount++
		}
	}
	return nil
}

func collectVersionRows(status *uicontrol.StorageStatus, root, cardSuffix string) error {
	kinds, exists, err := safeDirectoryEntries(root)
	if err != nil || !exists {
		return err
	}
	for _, kindEntry := range kinds {
		path := filepath.Join(root, kindEntry.Name())
		bytes, err := safeTreeBytes(path)
		if err != nil {
			return err
		}
		if len(status.Inventory) >= maxStorageInventoryRows {
			return errors.New("version inventory exceeds row limit")
		}
		status.Inventory = append(status.Inventory, uicontrol.SnapshotStatus{
			CardSuffix: cardSuffix, Category: "versions", Kind: inventoryKind(kindEntry.Name()),
			Name: folderKindLabel(inventoryKind(kindEntry.Name())) + " version history", Bytes: bytes,
		})
		status.VersionBytes += bytes
		status.VersionGroups++
	}
	return nil
}

func safeDirectoryEntries(path string) ([]fs.DirEntry, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, true, fmt.Errorf("inventory root is not a real directory: %s", path)
	}
	entries, err := os.ReadDir(path)
	return entries, true, err
}

func safeTreeBytes(path string) (int64, error) {
	var total int64
	err := filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("inventory contains a symlink: %s", current)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() < 0 || total > int64(^uint64(0)>>1)-info.Size() {
			return errors.New("inventory size overflows")
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func inventoryKind(value string) string {
	switch strings.ToLower(value) {
	case "saves":
		return "saves"
	case "states":
		return "states"
	default:
		return "other"
	}
}

func boundedInventoryName(value string) string {
	if len(value) > 128 {
		return value[:128]
	}
	if value == "" {
		return "Unnamed"
	}
	return value
}
