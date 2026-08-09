package syncthing

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
)

const maxFolderDevices = 33

type managedFolderDevice struct {
	DeviceID string `json:"deviceID"`
}

type managedFolderVersioning struct {
	Type             string            `json:"type"`
	Params           map[string]string `json:"params"`
	CleanupIntervalS int               `json:"cleanupIntervalS"`
	FSPath           string            `json:"fsPath"`
	FSType           string            `json:"fsType"`
}

type managedFolderRequest struct {
	ID             string                   `json:"id"`
	Label          string                   `json:"label"`
	FilesystemType string                   `json:"filesystemType"`
	Path           string                   `json:"path"`
	Type           string                   `json:"type"`
	Devices        []managedFolderDevice    `json:"devices"`
	IgnorePerms    bool                     `json:"ignorePerms"`
	Paused         bool                     `json:"paused"`
	MarkerName     string                   `json:"markerName"`
	Versioning     *managedFolderVersioning `json:"versioning,omitempty"`
}

// ConfiguredFolderDevices returns the exact configured device set for a new
// managed folder. The local device must be present and at least one remote peer
// must already be configured; pending devices are deliberately excluded.
func (process *Process) ConfiguredFolderDevices(ctx context.Context, selfDeviceID string) ([]string, error) {
	selfDeviceID, err := NormalizeDeviceID(selfDeviceID)
	if err != nil {
		return nil, errors.New("local Syncthing device id is invalid")
	}
	var devices []uiDevice
	if err := process.apiJSON(ctx, http.MethodGet, "/rest/config/devices", nil, &devices); err != nil {
		return nil, err
	}
	if len(devices) > maxFolderDevices {
		return nil, errors.New("configured device count exceeds the managed-folder limit")
	}
	seen := make(map[string]bool, len(devices))
	result := make([]string, 0, len(devices))
	selfPresent := false
	for _, device := range devices {
		deviceID, err := NormalizeDeviceID(device.DeviceID)
		if err != nil || seen[deviceID] {
			return nil, errors.New("configured device list is invalid or duplicated")
		}
		seen[deviceID] = true
		selfPresent = selfPresent || deviceID == selfDeviceID
		result = append(result, deviceID)
	}
	if !selfPresent {
		return nil, errors.New("configured device list does not contain the local device")
	}
	if len(result) < 2 {
		return nil, errors.New("add at least one Syncthing peer before creating a folder")
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left] == selfDeviceID {
			return true
		}
		if result[right] == selfDeviceID {
			return false
		}
		return result[left] < result[right]
	})
	return result, nil
}

// AddManagedFolder uses the supported runtime config API and always creates a
// folder paused. The controller is the only caller and has already confined
// the paths to an enrolled card; this layer independently bounds the request.
func (process *Process) AddManagedFolder(ctx context.Context, folder ConfiguredFolder) error {
	if len(folder.Devices) < 2 {
		return errors.New("managed folder requires the local device and at least one peer")
	}
	request, err := managedFolderAPIRequest(folder)
	if err != nil {
		return err
	}
	if err := process.apiJSON(ctx, http.MethodPost, "/rest/config/folders", request, nil); err != nil {
		return errors.New("add managed folder through upstream API: " + err.Error())
	}
	return nil
}

// SetManagedFolderType changes a managed folder only while leaving it paused.
// Receive-capable targets carry the explicit same-card versioning object.
func (process *Process) SetManagedFolderType(ctx context.Context, folder ConfiguredFolder) error {
	request, err := managedFolderAPIRequest(folder)
	if err != nil {
		return err
	}
	patch := map[string]any{"paused": true, "type": request.Type}
	if request.Versioning != nil {
		patch["versioning"] = request.Versioning
	}
	path := "/rest/config/folders/" + url.PathEscape(folder.ID)
	if err := process.apiJSON(ctx, http.MethodPatch, path, patch, nil); err != nil {
		return errors.New("change managed folder type through upstream API: " + err.Error())
	}
	return nil
}

// SetManagedFolderDevices atomically pauses a managed folder while replacing
// its exact membership, then verifies the resulting upstream configuration.
func (process *Process) SetManagedFolderDevices(ctx context.Context, folder ConfiguredFolder) error {
	request, err := managedFolderAPIRequest(folder)
	if err != nil {
		return err
	}
	path := "/rest/config/folders/" + url.PathEscape(folder.ID)
	patch := map[string]any{"paused": true, "devices": request.Devices}
	if err := process.apiJSON(ctx, http.MethodPatch, path, patch, nil); err != nil {
		return errors.New("change managed folder devices through upstream API: " + err.Error())
	}
	var current struct {
		Paused  bool                  `json:"paused"`
		Devices []managedFolderDevice `json:"devices"`
	}
	if err := process.apiJSON(ctx, http.MethodGet, path, nil, &current); err != nil {
		return errors.New("verify managed folder devices through upstream API: " + err.Error())
	}
	if !current.Paused || !sameManagedFolderDevices(current.Devices, request.Devices) {
		return errors.New("upstream managed folder devices did not match the requested membership")
	}
	return nil
}

func sameManagedFolderDevices(left, right []managedFolderDevice) bool {
	if len(left) != len(right) {
		return false
	}
	want := make(map[string]bool, len(right))
	for _, device := range right {
		want[device.DeviceID] = true
	}
	seen := make(map[string]bool, len(left))
	for _, device := range left {
		if !want[device.DeviceID] || seen[device.DeviceID] {
			return false
		}
		seen[device.DeviceID] = true
	}
	return true
}

func managedFolderAPIRequest(folder ConfiguredFolder) (managedFolderRequest, error) {
	if !ValidFolderID(folder.ID) || !filepath.IsAbs(folder.Path) ||
		folder.MarkerName == "" || folder.MarkerName == ".stfolder" || filepath.Base(folder.MarkerName) != folder.MarkerName ||
		!validDisplayName(strings.TrimSpace(folder.Label), maxFolderName) ||
		(folder.Type != "sendonly" && folder.Type != "sendreceive" && folder.Type != "receiveonly") {
		return managedFolderRequest{}, errors.New("managed folder request is invalid")
	}
	if len(folder.Devices) < 1 || len(folder.Devices) > maxFolderDevices {
		return managedFolderRequest{}, errors.New("managed folder requires the local device")
	}
	devices := make([]managedFolderDevice, 0, len(folder.Devices))
	seen := make(map[string]bool, len(folder.Devices))
	for _, rawDeviceID := range folder.Devices {
		deviceID, err := NormalizeDeviceID(rawDeviceID)
		if err != nil || seen[deviceID] {
			return managedFolderRequest{}, errors.New("managed folder device list is invalid or duplicated")
		}
		seen[deviceID] = true
		devices = append(devices, managedFolderDevice{DeviceID: deviceID})
	}
	request := managedFolderRequest{
		ID: folder.ID, Label: strings.TrimSpace(folder.Label), FilesystemType: "basic",
		Path: filepath.Clean(folder.Path), Type: folder.Type, Devices: devices,
		IgnorePerms: true, Paused: true, MarkerName: folder.MarkerName,
	}
	if folder.Type != "sendonly" {
		if folder.VersioningType != "simple" || folder.VersioningFSType != "basic" || !filepath.IsAbs(folder.VersioningFSPath) {
			return managedFolderRequest{}, errors.New("receive-capable managed folder versioning is invalid")
		}
		request.Versioning = &managedFolderVersioning{
			Type: "simple", Params: map[string]string{"keep": "5", "cleanoutDays": "0"},
			CleanupIntervalS: 3600, FSPath: filepath.Clean(folder.VersioningFSPath), FSType: "basic",
		}
	}
	return request, nil
}
