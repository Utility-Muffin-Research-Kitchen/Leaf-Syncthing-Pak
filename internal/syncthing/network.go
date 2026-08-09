package syncthing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"
)

type NetworkProfile string

const (
	NetworkLANOnly      NetworkProfile = "lan-only"
	NetworkSyncAnywhere NetworkProfile = "sync-anywhere"
)

var noEligibleNetwork = []string{"0.0.0.0/32", "::/128"}

type NetworkProfileRequest struct {
	Profile         NetworkProfile
	SelfDeviceID    string
	AllowedNetworks []string
}

type networkDevice struct {
	DeviceID        string   `json:"deviceID"`
	Paused          bool     `json:"paused"`
	AllowedNetworks []string `json:"allowedNetworks"`
}

type networkConnections struct {
	Connections map[string]struct {
		Connected bool   `json:"connected"`
		Address   string `json:"address"`
	} `json:"connections"`
}

func (request NetworkProfileRequest) validate() error {
	if request.Profile != NetworkLANOnly && request.Profile != NetworkSyncAnywhere {
		return fmt.Errorf("unsupported network profile %q", request.Profile)
	}
	if request.SelfDeviceID == "" {
		return errors.New("network profile requires the local device id")
	}
	for index := 1; index < len(request.AllowedNetworks); index++ {
		if request.AllowedNetworks[index-1] >= request.AllowedNetworks[index] {
			return errors.New("allowed networks must be sorted and unique")
		}
	}
	return nil
}

// ApplyNetworkProfile owns the live D-14 transition. Restoring LAN-only first
// pauses connected peers, waits for disconnect, applies the route-derived
// boundary, then restores only peers that were previously unpaused.
func (process *Process) ApplyNetworkProfile(ctx context.Context, request NetworkProfileRequest) error {
	if err := request.validate(); err != nil {
		return err
	}
	var devices []networkDevice
	if err := process.apiJSON(ctx, http.MethodGet, "/rest/config/devices", nil, &devices); err != nil {
		return err
	}
	remote := make([]networkDevice, 0, len(devices))
	for _, device := range devices {
		if device.DeviceID == "" {
			return errors.New("upstream returned a device without an id")
		}
		if device.DeviceID != request.SelfDeviceID {
			remote = append(remote, device)
		}
	}
	sort.Slice(remote, func(left, right int) bool { return remote[left].DeviceID < remote[right].DeviceID })

	if request.Profile == NetworkSyncAnywhere {
		for _, device := range remote {
			if err := process.patchDevice(ctx, device.DeviceID, map[string]any{"allowedNetworks": []string{}}); err != nil {
				return err
			}
		}
		return process.patchNetworkOptions(ctx, request.Profile)
	}
	allowedNetworks := request.AllowedNetworks
	if len(allowedNetworks) == 0 {
		allowedNetworks = noEligibleNetwork
	}

	pausedByTransition := make([]string, 0, len(remote))
	restore := func() error {
		var first error
		for _, deviceID := range pausedByTransition {
			if err := process.patchDevice(ctx, deviceID, map[string]any{"paused": false}); err != nil && first == nil {
				first = err
			}
		}
		return first
	}
	for _, device := range remote {
		if device.Paused {
			continue
		}
		if err := process.patchDevice(ctx, device.DeviceID, map[string]any{"paused": true}); err != nil {
			_ = restore()
			return err
		}
		pausedByTransition = append(pausedByTransition, device.DeviceID)
	}
	if err := process.waitDisconnected(ctx, remote, 5*time.Second); err != nil {
		_ = restore()
		return err
	}
	for _, device := range remote {
		if err := process.patchDevice(ctx, device.DeviceID,
			map[string]any{"allowedNetworks": append([]string(nil), allowedNetworks...)}); err != nil {
			_ = restore()
			return err
		}
	}
	if err := process.patchNetworkOptions(ctx, request.Profile); err != nil {
		_ = restore()
		return err
	}
	if err := restore(); err != nil {
		return fmt.Errorf("restore peer pause state: %w", err)
	}
	return nil
}

func (process *Process) patchDevice(ctx context.Context, deviceID string, patch map[string]any) error {
	if err := process.apiJSON(ctx, http.MethodPatch, deviceConfigPath(deviceID), patch, nil); err != nil {
		return fmt.Errorf("update peer %s: %w", deviceID, err)
	}
	return nil
}

func (process *Process) patchNetworkOptions(ctx context.Context, profile NetworkProfile) error {
	anywhere := profile == NetworkSyncAnywhere
	patch := map[string]any{
		"globalAnnounceEnabled": anywhere,
		"localAnnounceEnabled":  true,
		"relaysEnabled":         anywhere,
		"natEnabled":            anywhere,
		"urAccepted":            -1,
		"crashReportingEnabled": false,
		"listenAddresses":       []string{"default"},
	}
	if err := process.apiJSON(ctx, http.MethodPatch, "/rest/config/options", patch, nil); err != nil {
		return fmt.Errorf("update network options: %w", err)
	}
	return nil
}

func (process *Process) waitDisconnected(ctx context.Context, devices []networkDevice, timeout time.Duration) error {
	if len(devices) == 0 {
		return nil
	}
	deviceSet := make(map[string]struct{}, len(devices))
	for _, device := range devices {
		deviceSet[device.DeviceID] = struct{}{}
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		var state networkConnections
		if err := process.apiJSON(ctx, http.MethodGet, "/rest/system/connections", nil, &state); err != nil {
			return err
		}
		connected := false
		for deviceID, connection := range state.Connections {
			if _, tracked := deviceSet[deviceID]; tracked && connection.Connected {
				connected = true
				break
			}
		}
		if !connected {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("timed out waiting for peer sockets to disconnect")
		case <-ticker.C:
		}
	}
}
