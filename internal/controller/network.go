package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/leaf"
	syncthingconfig "github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/uicontrol"
)

const networkProfileStateName = "network-profile.json"

type networkUpstream interface {
	ApplyNetworkProfile(context.Context, syncthingconfig.NetworkProfileRequest) error
}

type deriveNetworksFunc func(syncthingconfig.RouteFiles) ([]string, error)

type networkProfileState struct {
	Schema  int    `json:"schema"`
	Profile string `json:"profile"`
}

type networkManager struct {
	mu         sync.Mutex
	upstream   networkUpstream
	selfID     string
	statePath  string
	routeFiles syncthingconfig.RouteFiles
	derive     deriveNetworksFunc
	profile    syncthingconfig.NetworkProfile
	allowed    []string
	observed   []string
}

func newNetworkManager(userdataPath, selfID string, upstream networkUpstream) (*networkManager, error) {
	if userdataPath == "" || selfID == "" || upstream == nil {
		return nil, errors.New("network manager requires state, device id, and upstream control")
	}
	manager := &networkManager{
		upstream: upstream, selfID: selfID,
		statePath:  filepath.Join(userdataPath, leaf.AppStateName, "leaf", networkProfileStateName),
		routeFiles: syncthingconfig.DefaultRouteFiles(), derive: syncthingconfig.DirectlyConnectedNetworks,
		profile: syncthingconfig.NetworkLANOnly,
	}
	state, err := readNetworkProfileState(manager.statePath)
	if err != nil {
		return nil, err
	}
	if state.Profile != "" {
		manager.profile = syncthingconfig.NetworkProfile(state.Profile)
	}
	return manager, nil
}

func (manager *networkManager) Initialize(ctx context.Context) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.applyLocked(ctx, manager.profile, true)
}

func (manager *networkManager) Set(ctx context.Context, profile string) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	candidate := syncthingconfig.NetworkProfile(profile)
	if candidate != syncthingconfig.NetworkLANOnly && candidate != syncthingconfig.NetworkSyncAnywhere {
		return fmt.Errorf("unsupported network profile %q", profile)
	}
	return manager.applyLocked(ctx, candidate, true)
}

func (manager *networkManager) RefreshIfChanged(ctx context.Context) (bool, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	routes, err := manager.derive(manager.routeFiles)
	if err != nil {
		return false, err
	}
	if slices.Equal(routes, manager.observed) {
		return false, nil
	}
	if manager.profile != syncthingconfig.NetworkLANOnly {
		manager.observed = append([]string(nil), routes...)
		return true, nil
	}
	if err := manager.applyWithNetworksLocked(ctx, manager.profile, routes, routes, false); err != nil {
		return false, err
	}
	return true, nil
}

func (manager *networkManager) Status() uicontrol.NetworkStatus {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return uicontrol.NetworkStatus{
		Profile: string(manager.profile), AllowedNetworks: append([]string(nil), manager.allowed...),
	}
}

func (manager *networkManager) applyLocked(ctx context.Context, profile syncthingconfig.NetworkProfile, persist bool) error {
	routes, err := manager.derive(manager.routeFiles)
	if err != nil {
		return err
	}
	var allowed []string
	if profile == syncthingconfig.NetworkLANOnly {
		allowed = routes
	}
	return manager.applyWithNetworksLocked(ctx, profile, allowed, routes, persist)
}

func (manager *networkManager) applyWithNetworksLocked(ctx context.Context, profile syncthingconfig.NetworkProfile, allowed, observed []string, persist bool) error {
	request := syncthingconfig.NetworkProfileRequest{
		Profile: profile, SelfDeviceID: manager.selfID,
		AllowedNetworks: append([]string(nil), allowed...),
	}
	if err := manager.upstream.ApplyNetworkProfile(ctx, request); err != nil {
		return err
	}
	if persist {
		if err := writeNetworkProfileState(manager.statePath, networkProfileState{Schema: 1, Profile: string(profile)}); err != nil {
			return err
		}
	}
	manager.profile = profile
	manager.allowed = append([]string(nil), allowed...)
	manager.observed = append([]string(nil), observed...)
	return nil
}

func readNetworkProfileState(path string) (networkProfileState, error) {
	var state networkProfileState
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 4096 {
		return state, errors.New("network profile state is unsafe")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return state, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return state, fmt.Errorf("decode network profile state: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return state, errors.New("network profile state contains trailing JSON")
	}
	if state.Schema != 1 || (state.Profile != string(syncthingconfig.NetworkLANOnly) &&
		state.Profile != string(syncthingconfig.NetworkSyncAnywhere)) {
		return state, errors.New("network profile state is unsupported")
	}
	return state, nil
}

func writeNetworkProfileState(path string, state networkProfileState) error {
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	temporary := path + ".tmp"
	if info, err := os.Lstat(temporary); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("network profile temporary is unsafe")
		}
		if err := os.Remove(temporary); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
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
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
