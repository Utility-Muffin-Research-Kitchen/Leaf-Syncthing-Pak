package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
)

const folderControlStateName = "folder-control.json"

type folderControlRecord struct {
	Manual        bool `json:"manual"`
	FirstSync     bool `json:"first_sync"`
	PendingRescan bool `json:"pending_rescan"`
}

type folderControlDocument struct {
	Schema  int                            `json:"schema"`
	Folders map[string]folderControlRecord `json:"folders"`
}

type folderControlStore struct {
	mu      sync.Mutex
	path    string
	records map[string]folderControlRecord
}

func newFolderControlStore(path string, folders []syncthing.ConfiguredFolder) (*folderControlStore, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("folder control state requires an absolute path")
	}
	records, present, err := readFolderControlState(path)
	if err != nil {
		return nil, err
	}
	changed := !present
	configured := make(map[string]bool, len(folders))
	for _, folder := range folders {
		configured[folder.ID] = true
		if _, ok := records[folder.ID]; !ok {
			records[folder.ID] = folderControlRecord{FirstSync: true}
			changed = true
		}
	}
	for folderID := range records {
		if !configured[folderID] {
			delete(records, folderID)
			changed = true
		}
	}
	store := &folderControlStore{path: path, records: records}
	if changed {
		if err := store.persistLocked(); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (store *folderControlStore) Snapshot() map[string]folderControlRecord {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make(map[string]folderControlRecord, len(store.records))
	for folderID, record := range store.records {
		result[folderID] = record
	}
	return result
}

func (store *folderControlStore) SetManual(folderID string, manual bool) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.records[folderID]
	if !ok {
		return errors.New("folder control state does not contain this folder")
	}
	record.Manual = manual
	store.records[folderID] = record
	return store.persistLocked()
}

func (store *folderControlStore) SetPendingRescan(folderID string, pending bool) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.records[folderID]
	if !ok {
		return errors.New("folder control state does not contain this folder")
	}
	record.PendingRescan = pending
	store.records[folderID] = record
	return store.persistLocked()
}

func readFolderControlState(path string) (map[string]folderControlRecord, bool, error) {
	records := make(map[string]folderControlRecord)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return records, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 64*1024 {
		return nil, false, errors.New("folder control state is unsafe")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	var document folderControlDocument
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, false, fmt.Errorf("decode folder control state: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || document.Schema != 1 ||
		document.Folders == nil || len(document.Folders) > 16 {
		return nil, false, errors.New("folder control state is unsupported")
	}
	for folderID, record := range document.Folders {
		if !stringsManagedFolderID(folderID) {
			return nil, false, errors.New("folder control state contains an invalid folder id")
		}
		records[folderID] = record
	}
	return records, true, nil
}

func (store *folderControlStore) persistLocked() error {
	keys := make([]string, 0, len(store.records))
	for key := range store.records {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	ordered := make(map[string]folderControlRecord, len(keys))
	for _, key := range keys {
		ordered[key] = store.records[key]
	}
	payload, err := json.Marshal(folderControlDocument{Schema: 1, Folders: ordered})
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return err
	}
	temporary := store.path + ".tmp"
	if info, err := os.Lstat(temporary); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("folder control temporary is unsafe")
		}
		if err := os.Remove(temporary); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(payload); err != nil {
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
	if err := os.Rename(temporary, store.path); err != nil {
		return err
	}
	return syncStateFilesystem(filepath.Dir(store.path))
}

func stringsManagedFolderID(value string) bool {
	prefixLength := 0
	switch {
	case len(value) == len("leaf-saves-")+16 && value[:len("leaf-saves-")] == "leaf-saves-":
		prefixLength = len("leaf-saves-")
	case len(value) == len("leaf-states-")+16 && value[:len("leaf-states-")] == "leaf-states-":
		prefixLength = len("leaf-states-")
	default:
		return false
	}
	for _, character := range value[prefixLength:] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
