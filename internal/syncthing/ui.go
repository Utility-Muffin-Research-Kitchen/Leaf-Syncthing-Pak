package syncthing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	maxPeerName   = 64
	maxFolderName = 96
)

type UIFolderStatus struct {
	ID           string
	State        string
	LocalBytes   int64
	GlobalBytes  int64
	LocalItems   int
	GlobalItems  int
	NeedBytes    int64
	ErrorCount   int
	PullErrors   int
	LastActivity string
}

type UIPeerStatus struct {
	ID           string
	Name         string
	State        string
	Connection   string
	Address      string
	Paused       bool
	Introducer   bool
	IntroducedBy string
	Pending      bool
}

type UITransferStatus struct {
	State       string
	LocalBytes  int64
	GlobalBytes int64
	NeedBytes   int64
	InBytes     int64
	OutBytes    int64
}

type UIStatus struct {
	Folders  map[string]UIFolderStatus
	Peers    []UIPeerStatus
	Transfer UITransferStatus
}

type uiDevice struct {
	DeviceID     string `json:"deviceID"`
	Name         string `json:"name"`
	Paused       bool   `json:"paused"`
	Introducer   bool   `json:"introducer"`
	IntroducedBy string `json:"introducedBy"`
}

type uiConnections struct {
	Connections map[string]struct {
		Connected bool   `json:"connected"`
		Paused    bool   `json:"paused"`
		Address   string `json:"address"`
		Type      string `json:"type"`
		IsLocal   bool   `json:"isLocal"`
		InBytes   int64  `json:"inBytesTotal"`
		OutBytes  int64  `json:"outBytesTotal"`
	} `json:"connections"`
}

type uiDBStatus struct {
	State             string `json:"state"`
	LocalBytes        int64  `json:"localBytes"`
	GlobalBytes       int64  `json:"globalBytes"`
	NeedBytes         int64  `json:"needBytes"`
	LocalFiles        int    `json:"localFiles"`
	LocalDirectories  int    `json:"localDirectories"`
	LocalSymlinks     int    `json:"localSymlinks"`
	GlobalFiles       int    `json:"globalFiles"`
	GlobalDirectories int    `json:"globalDirectories"`
	GlobalSymlinks    int    `json:"globalSymlinks"`
	Errors            int    `json:"errors"`
	PullErrors        int    `json:"pullErrors"`
	StateChange       string `json:"stateChanged"`
}

type uiFolderStats map[string]struct {
	LastFile struct {
		At time.Time `json:"at"`
	} `json:"lastFile"`
}

type uiPendingDevice struct {
	Name string `json:"name"`
}

// ReadUIStatus returns only bounded, display-safe data used by the C client.
// It deliberately does not return upstream config objects or secrets.
func (process *Process) ReadUIStatus(ctx context.Context, folders []ConfiguredFolder, selfDeviceID string) (UIStatus, error) {
	status := UIStatus{Folders: make(map[string]UIFolderStatus), Transfer: UITransferStatus{State: "idle"}}
	var devices []uiDevice
	if err := process.apiJSON(ctx, http.MethodGet, "/rest/config/devices", nil, &devices); err != nil {
		return UIStatus{}, err
	}
	var connections uiConnections
	if err := process.apiJSON(ctx, http.MethodGet, "/rest/system/connections", nil, &connections); err != nil {
		return UIStatus{}, err
	}
	var pending map[string]uiPendingDevice
	if err := process.apiJSON(ctx, http.MethodGet, "/rest/cluster/pending/devices", nil, &pending); err != nil {
		return UIStatus{}, err
	}
	var stats uiFolderStats
	if err := process.apiJSON(ctx, http.MethodGet, "/rest/stats/folder", nil, &stats); err != nil {
		return UIStatus{}, err
	}

	for _, folder := range folders {
		var upstream uiDBStatus
		path := "/rest/db/status?" + url.Values{"folder": []string{folder.ID}}.Encode()
		if err := process.apiJSON(ctx, http.MethodGet, path, nil, &upstream); err != nil {
			return UIStatus{}, err
		}
		row := UIFolderStatus{
			ID: folder.ID, State: boundedState(upstream.State), LocalBytes: nonnegative(upstream.LocalBytes),
			GlobalBytes: nonnegative(upstream.GlobalBytes), NeedBytes: nonnegative(upstream.NeedBytes),
			LocalItems:  boundedItemTotal(upstream.LocalFiles, upstream.LocalDirectories, upstream.LocalSymlinks),
			GlobalItems: boundedItemTotal(upstream.GlobalFiles, upstream.GlobalDirectories, upstream.GlobalSymlinks),
			ErrorCount:  nonnegativeInt(upstream.Errors), PullErrors: nonnegativeInt(upstream.PullErrors),
		}
		if entry, ok := stats[folder.ID]; ok && !entry.LastFile.At.IsZero() {
			row.LastActivity = entry.LastFile.At.UTC().Format(time.RFC3339)
		} else if parsed, err := time.Parse(time.RFC3339Nano, upstream.StateChange); err == nil {
			row.LastActivity = parsed.UTC().Format(time.RFC3339)
		}
		status.Folders[folder.ID] = row
		status.Transfer.LocalBytes += row.LocalBytes
		status.Transfer.GlobalBytes += row.GlobalBytes
		status.Transfer.NeedBytes += row.NeedBytes
		if row.State == "syncing" || row.State == "scanning" || row.State == "sync-preparing" {
			status.Transfer.State = row.State
		}
	}

	seen := make(map[string]bool)
	for _, device := range devices {
		if device.DeviceID == "" || device.DeviceID == selfDeviceID {
			continue
		}
		connection := connections.Connections[device.DeviceID]
		row := UIPeerStatus{
			ID: device.DeviceID, Name: displayPeerName(device.Name, device.DeviceID), Paused: device.Paused,
			Introducer: device.Introducer, IntroducedBy: device.IntroducedBy, Address: boundedAddress(connection.Address),
			State: "offline", Connection: "none",
		}
		if device.Paused || connection.Paused {
			row.State = "paused"
		} else if connection.Connected {
			row.State = "connected"
			row.Connection = connectionKind(connection.IsLocal, connection.Type, connection.Address)
		}
		status.Transfer.InBytes += nonnegative(connection.InBytes)
		status.Transfer.OutBytes += nonnegative(connection.OutBytes)
		status.Peers = append(status.Peers, row)
		seen[device.DeviceID] = true
	}
	for deviceID, device := range pending {
		if deviceID == "" || seen[deviceID] || deviceID == selfDeviceID {
			continue
		}
		status.Peers = append(status.Peers, UIPeerStatus{
			ID: deviceID, Name: displayPeerName(device.Name, deviceID), State: "pending",
			Connection: "none", Pending: true,
		})
	}
	sort.Slice(status.Peers, func(left, right int) bool {
		if status.Peers[left].Pending != status.Peers[right].Pending {
			return status.Peers[left].Pending
		}
		return status.Peers[left].Name < status.Peers[right].Name
	})
	return status, nil
}

func boundedItemTotal(values ...int) int {
	const maximum = 1_000_000_000
	total := 0
	for _, value := range values {
		value = nonnegativeInt(value)
		if value > maximum-total {
			return maximum
		}
		total += value
	}
	return total
}

func (process *Process) SetFolderPaused(ctx context.Context, folderID string, paused bool) error {
	if !ValidFolderID(folderID) {
		return errors.New("invalid managed folder id")
	}
	path := "/rest/config/folders/" + url.PathEscape(folderID)
	if err := process.apiJSON(ctx, http.MethodPatch, path, map[string]any{"paused": paused}, nil); err != nil {
		return fmt.Errorf("update folder pause state: %w", err)
	}
	return nil
}

func (process *Process) RescanFolder(ctx context.Context, folderID string) error {
	if !ValidFolderID(folderID) {
		return errors.New("invalid managed folder id")
	}
	path := "/rest/db/scan?" + url.Values{"folder": []string{folderID}}.Encode()
	if err := process.apiJSON(ctx, http.MethodPost, path, nil, nil); err != nil {
		return fmt.Errorf("rescan folder: %w", err)
	}
	return nil
}

func (process *Process) RenameFolder(ctx context.Context, folderID, label string) error {
	label = strings.TrimSpace(label)
	if !ValidFolderID(folderID) || !validDisplayName(label, maxFolderName) {
		return errors.New("invalid managed folder label")
	}
	path := "/rest/config/folders/" + url.PathEscape(folderID)
	if err := process.apiJSON(ctx, http.MethodPatch, path, map[string]any{"label": label}, nil); err != nil {
		return fmt.Errorf("rename folder: %w", err)
	}
	return nil
}

func (process *Process) AddPeer(ctx context.Context, rawDeviceID, name string, allowedNetworks []string) error {
	deviceID, err := NormalizeDeviceID(rawDeviceID)
	if err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Syncthing peer"
	}
	if !validDisplayName(name, maxPeerName) {
		return errors.New("invalid peer name")
	}
	request := map[string]any{
		"deviceID": deviceID, "name": name, "addresses": []string{"dynamic"},
		"paused": false, "introducer": false, "autoAcceptFolders": false,
		"allowedNetworks": append([]string(nil), allowedNetworks...),
	}
	if err := process.apiJSON(ctx, http.MethodPost, "/rest/config/devices", request, nil); err != nil {
		return fmt.Errorf("add peer: %w", err)
	}
	return nil
}

func (process *Process) RenamePeer(ctx context.Context, rawDeviceID, name string) error {
	deviceID, err := NormalizeDeviceID(rawDeviceID)
	if err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if !validDisplayName(name, maxPeerName) {
		return errors.New("invalid peer name")
	}
	return process.patchDevice(ctx, deviceID, map[string]any{"name": name})
}

func NormalizeDeviceID(value string) (string, error) {
	value = strings.TrimSpace(value)
	for _, prefix := range []string{"syncthing://device/", "syncthing://"} {
		if strings.HasPrefix(strings.ToLower(value), prefix) {
			value = value[len(prefix):]
			break
		}
	}
	if index := strings.IndexAny(value, "?#"); index >= 0 {
		value = value[:index]
	}
	value = strings.ToUpper(strings.TrimSpace(value))
	parts := strings.Split(value, "-")
	if len(parts) != 8 {
		return "", errors.New("device id must contain eight groups")
	}
	for _, part := range parts {
		if len(part) != 7 {
			return "", errors.New("device id group has the wrong length")
		}
		for _, character := range part {
			if (character < 'A' || character > 'Z') && (character < '2' || character > '7') {
				return "", errors.New("device id contains an invalid character")
			}
		}
	}
	return value, nil
}

func connectionKind(local bool, connectionType, address string) string {
	combined := strings.ToLower(connectionType + " " + address)
	if strings.Contains(combined, "relay") {
		return "relay"
	}
	if local {
		return "local"
	}
	return "direct"
}

func displayPeerName(name, deviceID string) string {
	name = strings.TrimSpace(name)
	if validDisplayName(name, maxPeerName) {
		return name
	}
	if len(deviceID) >= 7 {
		return "Peer " + deviceID[:7]
	}
	return "Peer"
}

func validDisplayName(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func boundedState(value string) string {
	switch value {
	case "idle", "scanning", "scan-waiting", "sync-waiting", "sync-preparing", "syncing", "cleaning", "clean-waiting", "error", "unknown", "paused":
		return value
	default:
		return "unknown"
	}
}

func boundedAddress(value string) string {
	if len(value) > 192 {
		return value[:192]
	}
	return value
}

func nonnegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func nonnegativeInt(value int) int {
	if value < 0 {
		return 0
	}
	return value
}
