package controller

import (
	"context"
	"errors"
	"sort"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/cards"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
)

func hasPendingMembership(records map[string]folderControlRecord) bool {
	for _, record := range records {
		if record.PendingMembership != "" {
			return true
		}
	}
	return false
}

func folderWithMembership(folder syncthing.ConfiguredFolder, selfDeviceID, peerDeviceID string, present bool) (syncthing.ConfiguredFolder, bool, error) {
	self, err := syncthing.NormalizeDeviceID(selfDeviceID)
	if err != nil {
		return folder, false, errors.New("the local Syncthing device identity is invalid")
	}
	peer, err := syncthing.NormalizeDeviceID(peerDeviceID)
	if err != nil || peer == self {
		return folder, false, errors.New("the selected peer identity is invalid")
	}
	members := make(map[string]bool, len(folder.Devices)+1)
	for _, rawDeviceID := range folder.Devices {
		deviceID, normalizeErr := syncthing.NormalizeDeviceID(rawDeviceID)
		if normalizeErr != nil || members[deviceID] {
			return folder, false, errors.New("the managed folder device list is invalid")
		}
		members[deviceID] = true
	}
	if !members[self] {
		return folder, false, errors.New("the managed folder does not contain this device")
	}
	changed := members[peer] != present
	members[peer] = present
	if !present {
		delete(members, peer)
	}
	peers := make([]string, 0, len(members)-1)
	for deviceID := range members {
		if deviceID != self {
			peers = append(peers, deviceID)
		}
	}
	sort.Strings(peers)
	folder.Devices = append([]string{self}, peers...)
	folder.Paused = true
	return folder, changed, nil
}

func recoverFolderMemberships(ctx context.Context, folders []syncthing.ConfiguredFolder, selfDeviceID string, inventory []cards.Card, controls *folderControlStore, upstream b3FolderUpstream) ([]syncthing.ConfiguredFolder, error) {
	result := append([]syncthing.ConfiguredFolder(nil), folders...)
	recovered := make(map[string]bool)
	for folderID, record := range controls.Snapshot() {
		if record.PendingMembership == "" {
			continue
		}
		folder, index, ok := findConfiguredFolder(result, folderID)
		if !ok {
			return nil, errors.New("pending folder membership change has no configured folder")
		}
		updated, _, err := folderWithMembership(folder, selfDeviceID, record.PendingDeviceID, record.PendingMembership == "share")
		if err != nil {
			return nil, err
		}
		if err := upstream.SetManagedFolderDevices(ctx, updated); err != nil {
			return nil, err
		}
		result[index] = updated
		if err := controls.CompleteMembership(folderID); err != nil {
			return nil, err
		}
		recovered[folderID] = true
	}
	if len(recovered) == 0 {
		return result, nil
	}
	rows, _ := reconcileManagedFolders(result, inventory, controls.Snapshot())
	for _, row := range rows {
		if !recovered[row.ID] {
			continue
		}
		if err := upstream.SetFolderPaused(ctx, row.ID, row.Paused); err != nil {
			return nil, err
		}
		_, index, _ := findConfiguredFolder(result, row.ID)
		result[index].Paused = row.Paused
	}
	return result, nil
}
