package cards

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/leaf"
)

const (
	RegistryVersion  = 1
	RegistryFileName = "cards-v1.json"
	maxRegistryBytes = 1024 * 1024
	maxRegistryCards = 16
)

type RegistryRecord struct {
	ID            string `json:"id"`
	LastSourceID  string `json:"last_source_id"`
	RetainedBytes int64  `json:"retained_bytes"`
}

type registryFile struct {
	Version int              `json:"version"`
	Cards   []RegistryRecord `json:"cards"`
}

// ReconcileRegistry recovers the primary registry, records uniquely observed
// enrolled cards, and adds identity-bearing rows for cards that are absent.
// The remembered slot is display/reconciliation metadata, never a write path.
func ReconcileRegistry(directory string, sources leaf.SourceList, live []Card, syncFilesystem func(string) error) ([]Card, error) {
	if syncFilesystem == nil {
		syncFilesystem = syncFilesystemAt
	}
	registry, err := recoverRegistry(directory, syncFilesystem)
	if err != nil {
		return nil, err
	}
	original, err := json.Marshal(registry)
	if err != nil {
		return nil, err
	}

	records := make(map[string]RegistryRecord, len(registry.Cards))
	for _, record := range registry.Cards {
		records[record.ID] = record
	}
	liveCounts := make(map[string]int)
	for _, card := range live {
		if card.Identity.ID != "" && (card.State == StateEnrolled || card.State == StateDuplicate) {
			liveCounts[card.Identity.ID]++
		}
	}
	for _, card := range live {
		if card.Identity.ID == "" || liveCounts[card.Identity.ID] != 1 || card.State != StateEnrolled {
			continue
		}
		records[card.Identity.ID] = RegistryRecord{
			ID: card.Identity.ID, LastSourceID: card.Source.ID, RetainedBytes: card.RetainedBytes,
		}
	}

	registry.Cards = registry.Cards[:0]
	for _, record := range records {
		registry.Cards = append(registry.Cards, record)
	}
	sort.Slice(registry.Cards, func(left, right int) bool { return registry.Cards[left].ID < registry.Cards[right].ID })
	updated, err := json.Marshal(registry)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(original, updated) {
		if err := writeRegistry(directory, registry, syncFilesystem); err != nil {
			return nil, err
		}
	}

	inventory := append([]Card(nil), live...)
	for _, record := range registry.Cards {
		if liveCounts[record.ID] > 0 {
			continue
		}
		source := sourceByID(sources, record.LastSourceID)
		remembered := Card{
			Source: source, Identity: Identity{Version: IdentityVersion, ID: record.ID},
			State: StateAbsent, RetainedBytes: record.RetainedBytes,
			Issues: []Issue{{Code: "card-absent", Message: "This enrolled card is not mounted"}},
		}
		replaced := false
		for index := range inventory {
			if inventory[index].Source.ID == record.LastSourceID && inventory[index].State == StateAbsent && inventory[index].Identity.ID == "" {
				inventory[index] = remembered
				replaced = true
				break
			}
		}
		if !replaced {
			inventory = append(inventory, remembered)
		}
	}
	return inventory, nil
}

func recoverRegistry(directory string, syncFilesystem func(string) error) (registryFile, error) {
	info, err := os.Lstat(directory)
	if err != nil {
		return registryFile{}, fmt.Errorf("inspect card registry directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return registryFile{}, errors.New("card registry directory is not a real directory")
	}
	path := filepath.Join(directory, RegistryFileName)
	temporary := path + ".tmp"
	backup := path + ".bak"
	current, currentExists, currentErr := readRegistry(path)
	_, temporaryExists, temporaryErr := readRegistry(temporary)
	backedUp, backupExists, backupErr := readRegistry(backup)

	if currentErr == nil && currentExists {
		if temporaryExists || temporaryErr != nil {
			if err := removeRegular(temporary); err != nil {
				return registryFile{}, err
			}
		}
		if backupErr != nil {
			if err := removeRegular(backup); err != nil {
				return registryFile{}, err
			}
		}
		if err := syncFilesystem(directory); err != nil {
			return registryFile{}, fmt.Errorf("confirm card registry filesystem: %w", err)
		}
		return current, nil
	}
	if backupErr == nil && backupExists {
		if currentExists || currentErr != nil {
			if err := removeRegular(path); err != nil {
				return registryFile{}, err
			}
		}
		if temporaryExists || temporaryErr != nil {
			if err := removeRegular(temporary); err != nil {
				return registryFile{}, err
			}
		}
		if err := os.Rename(backup, path); err != nil {
			return registryFile{}, fmt.Errorf("restore card registry backup: %w", err)
		}
		if err := syncFilesystem(directory); err != nil {
			return registryFile{}, fmt.Errorf("flush restored card registry: %w", err)
		}
		return backedUp, nil
	}
	if !currentExists && currentErr == nil && !backupExists && backupErr == nil && (temporaryExists || temporaryErr != nil) {
		if err := removeRegular(temporary); err != nil {
			return registryFile{}, err
		}
		if err := syncFilesystem(directory); err != nil {
			return registryFile{}, fmt.Errorf("flush discarded initial card registry temporary: %w", err)
		}
		return registryFile{Version: RegistryVersion, Cards: []RegistryRecord{}}, nil
	}
	if currentExists || temporaryExists || backupExists || currentErr != nil || temporaryErr != nil || backupErr != nil {
		return registryFile{}, errors.New("card registry has no known-good copy")
	}
	return registryFile{Version: RegistryVersion, Cards: []RegistryRecord{}}, nil
}

func writeRegistry(directory string, registry registryFile, syncFilesystem func(string) error) error {
	path := filepath.Join(directory, RegistryFileName)
	temporary := path + ".tmp"
	backup := path + ".bak"
	payload, err := json.Marshal(registry)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create card registry temporary: %w", err)
	}
	if _, err := io.Copy(file, bytes.NewReader(payload)); err != nil {
		_ = file.Close()
		return fmt.Errorf("write card registry temporary: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("flush card registry temporary: %w", err)
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := removeRegularIfExists(backup); err != nil {
		return err
	}
	if _, exists, err := readRegistry(path); err != nil {
		return err
	} else if exists {
		if err := os.Rename(path, backup); err != nil {
			return fmt.Errorf("backup card registry: %w", err)
		}
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("promote card registry: %w", err)
	}
	if err := syncFilesystem(directory); err != nil {
		return fmt.Errorf("flush card registry: %w", err)
	}
	if _, exists, err := readRegistry(path); err != nil {
		return fmt.Errorf("validate promoted card registry: %w", err)
	} else if !exists {
		return errors.New("validate promoted card registry: file is absent")
	}
	return nil
}

func readRegistry(path string) (registryFile, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return registryFile{}, false, nil
	}
	if err != nil {
		return registryFile{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxRegistryBytes {
		return registryFile{}, true, errors.New("card registry file is unsafe")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return registryFile{}, true, err
	}
	var registry registryFile
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&registry); err != nil {
		return registryFile{}, true, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return registryFile{}, true, errors.New("card registry contains trailing JSON")
	}
	if registry.Version != RegistryVersion || registry.Cards == nil || len(registry.Cards) > maxRegistryCards {
		return registryFile{}, true, errors.New("card registry schema or size is invalid")
	}
	seen := make(map[string]bool)
	for _, record := range registry.Cards {
		if !validIdentityID(record.ID) || record.LastSourceID == "" || record.RetainedBytes < 0 || seen[record.ID] {
			return registryFile{}, true, errors.New("card registry record is invalid")
		}
		seen[record.ID] = true
	}
	return registry, true, nil
}

func removeRegularIfExists(path string) error {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	}
	return removeRegular(path)
}

func removeRegular(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("refuse to remove unsafe card registry file")
	}
	return os.Remove(path)
}

func sourceByID(sources leaf.SourceList, id string) leaf.Source {
	for _, source := range sources {
		if source.ID == id {
			return source
		}
	}
	return leaf.Source{ID: id}
}
