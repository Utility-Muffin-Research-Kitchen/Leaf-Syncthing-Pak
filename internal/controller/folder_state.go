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

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/cards"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
)

const folderControlStateName = "folder-control.json"

type folderControlRecord struct {
	CardID            string `json:"card_id"`
	Kind              string `json:"kind"`
	MarkerName        string `json:"marker_name"`
	Manual            bool   `json:"manual"`
	FirstSync         bool   `json:"first_sync"`
	FirstSyncEpoch    uint64 `json:"first_sync_epoch"`
	PendingRescan     bool   `json:"pending_rescan"`
	PendingAdd        bool   `json:"pending_add,omitempty"`
	PendingMembership string `json:"pending_membership,omitempty"`
	PendingDeviceID   string `json:"pending_device_id,omitempty"`
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

func newFolderControlStore(path string, folders []syncthing.ConfiguredFolder, inventory []cards.Card) (*folderControlStore, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("folder control state requires an absolute path")
	}
	records, schema, present, err := readFolderControlState(path)
	if err != nil {
		return nil, err
	}
	changed := !present || schema == 1
	configured := make(map[string]bool, len(folders))
	for _, folder := range folders {
		configured[folder.ID] = true
		record, ok := records[folder.ID]
		if !ok {
			record = folderControlRecord{FirstSync: true, FirstSyncEpoch: 1}
			changed = true
		} else if record.FirstSyncEpoch == 0 {
			record.FirstSyncEpoch = 1
			changed = true
		}
		if !completeFolderBinding(record) {
			binding, err := legacyFolderBinding(folder, inventory)
			if err != nil {
				return nil, err
			}
			record.CardID = binding.CardID
			record.Kind = binding.Kind
			record.MarkerName = binding.MarkerName
			changed = true
		}
		records[folder.ID] = record
	}
	if schema == 1 {
		for folderID := range records {
			if configured[folderID] {
				continue
			}
			delete(records, folderID)
			changed = true
		}
	}
	if err := validateFolderControlRecords(records); err != nil {
		return nil, err
	}
	store := &folderControlStore{path: path, records: records}
	if changed {
		if err := store.persistLocked(); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (store *folderControlStore) BindingKinds() map[string]string {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make(map[string]string, len(store.records))
	for folderID, record := range store.records {
		result[folderID] = record.Kind
	}
	return result
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

func (store *folderControlStore) SetFirstSync(folderID string, pending bool) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.records[folderID]
	if !ok {
		return errors.New("folder control state does not contain this folder")
	}
	if record.FirstSync == pending {
		return nil
	}
	record.FirstSync = pending
	store.records[folderID] = record
	return store.persistLocked()
}

func (store *folderControlStore) RequireFirstSync(folderID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.records[folderID]
	if !ok {
		return errors.New("folder control state does not contain this folder")
	}
	if record.FirstSyncEpoch == ^uint64(0) {
		return errors.New("folder first-sync epoch is exhausted")
	}
	record.FirstSync = true
	record.FirstSyncEpoch++
	store.records[folderID] = record
	return store.persistLocked()
}

func (store *folderControlStore) Add(folder syncthing.ConfiguredFolder, card cards.Card) error {
	return store.add(folder, card, false)
}

func (store *folderControlStore) BeginAdd(folder syncthing.ConfiguredFolder, card cards.Card) error {
	return store.add(folder, card, true)
}

func (store *folderControlStore) add(folder syncthing.ConfiguredFolder, card cards.Card, pending bool) error {
	record, err := newFolderControlRecord(folder, card)
	if err != nil {
		return err
	}
	record.PendingAdd = pending
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.records[folder.ID]; ok {
		return errors.New("folder control state already contains this folder")
	}
	if len(store.records) >= 16 {
		return errors.New("folder control state exceeds the managed-folder limit")
	}
	for _, existing := range store.records {
		if existing.CardID == record.CardID && existing.Kind == record.Kind {
			return errors.New("folder control state already binds this card and content kind")
		}
	}
	store.records[folder.ID] = record
	if err := store.persistLocked(); err != nil {
		delete(store.records, folder.ID)
		return err
	}
	return nil
}

func (store *folderControlStore) Activate(folderID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.records[folderID]
	if !ok {
		return errors.New("folder control state does not contain this folder")
	}
	if !record.PendingAdd {
		return nil
	}
	record.PendingAdd = false
	store.records[folderID] = record
	if err := store.persistLocked(); err != nil {
		record.PendingAdd = true
		store.records[folderID] = record
		return err
	}
	return nil
}

func (store *folderControlStore) Remove(folderID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.records[folderID]
	if !ok {
		return nil
	}
	delete(store.records, folderID)
	if err := store.persistLocked(); err != nil {
		store.records[folderID] = record
		return err
	}
	return nil
}

func (store *folderControlStore) BeginMembership(folderID, deviceID, operation string) error {
	deviceID, err := syncthing.NormalizeDeviceID(deviceID)
	if err != nil || (operation != "share" && operation != "unshare") {
		return errors.New("folder membership intent is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.records[folderID]
	if !ok || record.PendingAdd {
		return errors.New("folder control state cannot change membership for this folder")
	}
	if record.PendingMembership != "" {
		if record.PendingMembership == operation && record.PendingDeviceID == deviceID {
			return nil
		}
		return errors.New("another folder membership change is pending")
	}
	record.PendingMembership = operation
	record.PendingDeviceID = deviceID
	store.records[folderID] = record
	if err := store.persistLocked(); err != nil {
		record.PendingMembership = ""
		record.PendingDeviceID = ""
		store.records[folderID] = record
		return err
	}
	return nil
}

func (store *folderControlStore) CompleteMembership(folderID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.records[folderID]
	if !ok || record.PendingMembership == "" {
		return errors.New("folder membership intent is absent")
	}
	original := record
	record.PendingMembership = ""
	record.PendingDeviceID = ""
	store.records[folderID] = record
	if err := store.persistLocked(); err != nil {
		store.records[folderID] = original
		return err
	}
	return nil
}

func readFolderControlState(path string) (map[string]folderControlRecord, int, bool, error) {
	records := make(map[string]folderControlRecord)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return records, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 64*1024 {
		return nil, 0, false, errors.New("folder control state is unsafe")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, false, err
	}
	var document folderControlDocument
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, 0, false, fmt.Errorf("decode folder control state: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || (document.Schema != 1 && document.Schema != 2) ||
		document.Folders == nil || len(document.Folders) > 16 {
		return nil, 0, false, errors.New("folder control state is unsupported")
	}
	for folderID, record := range document.Folders {
		if (document.Schema == 1 && !stringsManagedFolderID(folderID)) ||
			(document.Schema == 2 && !syncthing.ValidFolderID(folderID)) {
			return nil, 0, false, errors.New("folder control state contains an invalid folder id")
		}
		records[folderID] = record
	}
	return records, document.Schema, true, nil
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
	payload, err := json.Marshal(folderControlDocument{Schema: 2, Folders: ordered})
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

func newFolderControlRecord(folder syncthing.ConfiguredFolder, card cards.Card) (folderControlRecord, error) {
	if !syncthing.ValidFolderID(folder.ID) {
		return folderControlRecord{}, errors.New("folder control state contains an invalid folder id")
	}
	_, markerName, err := cards.BindingNames(card.Identity.ID, folder.Kind)
	if err != nil || markerName != folder.MarkerName {
		return folderControlRecord{}, errors.New("folder control state binding does not match its physical card")
	}
	return folderControlRecord{
		CardID: card.Identity.ID, Kind: folder.Kind, MarkerName: markerName,
		FirstSync: true, FirstSyncEpoch: 1,
	}, nil
}

func legacyFolderBinding(folder syncthing.ConfiguredFolder, inventory []cards.Card) (folderControlRecord, error) {
	var binding folderControlRecord
	matches := 0
	for _, card := range inventory {
		folderID, markerName, err := cards.BindingNames(card.Identity.ID, folder.Kind)
		if err == nil && folderID == folder.ID && markerName == folder.MarkerName {
			binding.CardID = card.Identity.ID
			binding.Kind = folder.Kind
			binding.MarkerName = markerName
			matches++
		}
	}
	if matches != 1 {
		return folderControlRecord{}, errors.New("folder control state cannot uniquely migrate its physical card binding")
	}
	return binding, nil
}

func completeFolderBinding(record folderControlRecord) bool {
	return record.CardID != "" && record.Kind != "" && record.MarkerName != ""
}

func validateFolderControlRecords(records map[string]folderControlRecord) error {
	localBindings := make(map[string]bool, len(records))
	for folderID, record := range records {
		if !syncthing.ValidFolderID(folderID) || !completeFolderBinding(record) {
			return errors.New("folder control state contains an incomplete binding")
		}
		_, markerName, err := cards.BindingNames(record.CardID, record.Kind)
		if err != nil || markerName != record.MarkerName {
			return errors.New("folder control state contains an invalid physical binding")
		}
		if record.PendingAdd && record.PendingMembership != "" ||
			(record.PendingMembership == "") != (record.PendingDeviceID == "") ||
			(record.PendingMembership != "" && record.PendingMembership != "share" && record.PendingMembership != "unshare") {
			return errors.New("folder control state contains an invalid pending mutation")
		}
		if record.PendingDeviceID != "" {
			if _, err := syncthing.NormalizeDeviceID(record.PendingDeviceID); err != nil {
				return errors.New("folder control state contains an invalid pending device")
			}
		}
		localKey := record.CardID + ":" + record.Kind
		if localBindings[localKey] {
			return errors.New("folder control state contains duplicate local bindings")
		}
		localBindings[localKey] = true
	}
	return nil
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
