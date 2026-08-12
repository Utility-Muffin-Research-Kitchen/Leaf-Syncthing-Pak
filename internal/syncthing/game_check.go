package syncthing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
)

// GameCheckStatus is the bounded freshness evidence used before Jawaka stops
// this supervised generation. Current is true only when every selected peer is
// connected and current and the local database has no pending work.
type GameCheckStatus struct {
	Current      bool
	PendingItems int
	PendingBytes int64
}

type gameCheckPeerRead struct {
	deviceID   string
	waiting    bool
	completion uiCompletion
	err        error
}

type gameCheckFolderRead struct {
	local    uiDBStatus
	localErr error
	peers    []gameCheckPeerRead
}

func (process *Process) ReadGameCheckStatus(ctx context.Context, folders []ConfiguredFolder, selfDeviceID string) (GameCheckStatus, error) {
	for _, folder := range folders {
		if folder.Paused {
			return GameCheckStatus{}, fmt.Errorf("folder %s is paused", folder.ID)
		}
	}
	var devices []uiDevice
	var connections uiConnections
	var devicesErr, connectionsErr error
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		devicesErr = process.apiJSON(ctx, http.MethodGet, "/rest/config/devices", nil, &devices)
	}()
	go func() {
		defer wait.Done()
		connectionsErr = process.apiJSON(ctx, http.MethodGet, "/rest/system/connections", nil, &connections)
	}()
	wait.Wait()
	if devicesErr != nil {
		return GameCheckStatus{}, devicesErr
	}
	if connectionsErr != nil {
		return GameCheckStatus{}, connectionsErr
	}
	configuredDevices := make(map[string]uiDevice, len(devices))
	for _, device := range devices {
		configuredDevices[device.DeviceID] = device
	}
	waitingPeers := make(map[string]bool)
	for _, folder := range folders {
		for _, deviceID := range folder.Devices {
			if deviceID == selfDeviceID {
				continue
			}
			device, configured := configuredDevices[deviceID]
			connection, found := connections.Connections[deviceID]
			if !configured {
				return GameCheckStatus{}, fmt.Errorf("folder %s peer is not configured", folder.ID)
			}
			if device.Paused || (found && connection.Paused) {
				return GameCheckStatus{}, fmt.Errorf("folder %s peer is paused", folder.ID)
			}
			if !found || !connection.Connected {
				waitingPeers[deviceID] = true
			}
		}
	}
	reads := make([]gameCheckFolderRead, len(folders))
	for folderIndex, folder := range folders {
		read := &reads[folderIndex]
		for _, deviceID := range folder.Devices {
			if deviceID != selfDeviceID {
				read.peers = append(read.peers, gameCheckPeerRead{
					deviceID: deviceID, waiting: waitingPeers[deviceID],
				})
			}
		}
		localPath := "/rest/db/status?" + url.Values{"folder": []string{folder.ID}}.Encode()
		wait.Add(1)
		go func() {
			defer wait.Done()
			read.localErr = process.apiJSON(ctx, http.MethodGet, localPath, nil, &read.local)
		}()
		for peerIndex := range read.peers {
			peerRead := &read.peers[peerIndex]
			if peerRead.waiting {
				continue
			}
			completionPath := "/rest/db/completion?" + url.Values{
				"device": []string{peerRead.deviceID}, "folder": []string{folder.ID},
			}.Encode()
			wait.Add(1)
			go func() {
				defer wait.Done()
				peerRead.err = process.apiJSON(ctx, http.MethodGet, completionPath, nil, &peerRead.completion)
			}()
		}
	}
	wait.Wait()
	result := GameCheckStatus{Current: true}
	for folderIndex, folder := range folders {
		read := reads[folderIndex]
		if read.localErr != nil {
			return GameCheckStatus{}, read.localErr
		}
		local := read.local
		if local.Errors > 0 || local.PullErrors > 0 {
			return GameCheckStatus{}, fmt.Errorf("folder %s reports sync errors", folder.ID)
		}
		localState := boundedState(local.State)
		if localState == "error" || localState == "unknown" || localState == "paused" ||
			localState == "scanning" || localState == "scan-waiting" || localState == "cleaning" || localState == "clean-waiting" {
			return GameCheckStatus{}, fmt.Errorf("folder %s state is %s", folder.ID, localState)
		}
		localNeedBytes := nonnegative(local.NeedBytes)
		localNeedItems := nonnegativeInt(local.NeedTotalItems)
		folderPending := localNeedBytes > 0 || localNeedItems > 0
		result.PendingBytes = boundedByteTotal(result.PendingBytes, localNeedBytes)
		result.PendingItems = boundedItemTotal(result.PendingItems, localNeedItems)

		for _, peerRead := range read.peers {
			if peerRead.waiting {
				folderPending = true
				result.PendingItems = boundedItemTotal(result.PendingItems, 1)
				continue
			}
			if peerRead.err != nil {
				return GameCheckStatus{}, peerRead.err
			}
			completion := peerRead.completion
			remoteNeedBytes := nonnegative(completion.NeedBytes)
			remoteNeedItems := boundedItemTotal(completion.NeedItems, completion.NeedDeletes)
			if completion.RemoteState == "unknown" {
				// Connections can report a peer before Syncthing finishes the
				// cluster handshake. Keep the launch waiting until that state is
				// valid; a sentinel item makes the unresolved safety condition a
				// valid LIFE-1 waiting reply even when need counts are not ready.
				if remoteNeedBytes == 0 && remoteNeedItems == 0 {
					remoteNeedItems = 1
				}
			} else if completion.RemoteState != "valid" {
				return GameCheckStatus{}, fmt.Errorf("folder %s peer state is not current", folder.ID)
			}
			folderPending = folderPending || remoteNeedBytes > 0 || remoteNeedItems > 0
			result.PendingBytes = boundedByteTotal(result.PendingBytes, remoteNeedBytes)
			result.PendingItems = boundedItemTotal(result.PendingItems, remoteNeedItems)
		}
		if localState != "idle" && !folderPending {
			return GameCheckStatus{}, errors.New("folder activity has no trustworthy pending count")
		}
	}
	result.Current = result.PendingItems == 0 && result.PendingBytes == 0
	return result, nil
}
