package syncthing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// GameCheckStatus is the bounded freshness evidence used before Jawaka stops
// this supervised generation. Current is true only when every selected peer is
// connected and current and the local database has no pending work.
type GameCheckStatus struct {
	Current      bool
	PendingItems int
	PendingBytes int64
}

func (process *Process) ReadGameCheckStatus(ctx context.Context, folders []ConfiguredFolder, selfDeviceID string) (GameCheckStatus, error) {
	var devices []uiDevice
	if err := process.apiJSON(ctx, http.MethodGet, "/rest/config/devices", nil, &devices); err != nil {
		return GameCheckStatus{}, err
	}
	var connections uiConnections
	if err := process.apiJSON(ctx, http.MethodGet, "/rest/system/connections", nil, &connections); err != nil {
		return GameCheckStatus{}, err
	}
	configuredDevices := make(map[string]uiDevice, len(devices))
	for _, device := range devices {
		configuredDevices[device.DeviceID] = device
	}
	result := GameCheckStatus{Current: true}
	for _, folder := range folders {
		if folder.Paused {
			return GameCheckStatus{}, fmt.Errorf("folder %s is paused", folder.ID)
		}
		var local uiDBStatus
		path := "/rest/db/status?" + url.Values{"folder": []string{folder.ID}}.Encode()
		if err := process.apiJSON(ctx, http.MethodGet, path, nil, &local); err != nil {
			return GameCheckStatus{}, err
		}
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

		for _, deviceID := range folder.Devices {
			if deviceID == selfDeviceID {
				continue
			}
			device, configured := configuredDevices[deviceID]
			connection, found := connections.Connections[deviceID]
			if !configured || !found || !connection.Connected {
				return GameCheckStatus{}, fmt.Errorf("folder %s peer is offline", folder.ID)
			}
			if device.Paused || connection.Paused {
				return GameCheckStatus{}, fmt.Errorf("folder %s peer is paused", folder.ID)
			}
			var completion uiCompletion
			completionPath := "/rest/db/completion?" + url.Values{
				"device": []string{deviceID}, "folder": []string{folder.ID},
			}.Encode()
			if err := process.apiJSON(ctx, http.MethodGet, completionPath, nil, &completion); err != nil {
				return GameCheckStatus{}, err
			}
			if completion.RemoteState != "valid" {
				return GameCheckStatus{}, fmt.Errorf("folder %s peer state is not current", folder.ID)
			}
			remoteNeedBytes := nonnegative(completion.NeedBytes)
			remoteNeedItems := boundedItemTotal(completion.NeedItems, completion.NeedDeletes)
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
