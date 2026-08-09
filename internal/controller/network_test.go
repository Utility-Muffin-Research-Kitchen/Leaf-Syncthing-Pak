package controller

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	syncthingconfig "github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
)

type fakeNetworkUpstream struct {
	requests []syncthingconfig.NetworkProfileRequest
}

func (upstream *fakeNetworkUpstream) ApplyNetworkProfile(_ context.Context, request syncthingconfig.NetworkProfileRequest) error {
	request.AllowedNetworks = append([]string(nil), request.AllowedNetworks...)
	upstream.requests = append(upstream.requests, request)
	return nil
}

func TestNetworkManagerRoundTripAndRouteRefresh(t *testing.T) {
	directory := t.TempDir()
	upstream := &fakeNetworkUpstream{}
	routes := []string{"192.168.1.0/24"}
	manager := &networkManager{
		upstream: upstream, selfID: "SELF",
		statePath: filepath.Join(directory, networkProfileStateName),
		derive: func(syncthingconfig.RouteFiles) ([]string, error) {
			return append([]string(nil), routes...), nil
		},
		profile: syncthingconfig.NetworkLANOnly,
	}
	if err := manager.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := manager.Status(); got.Profile != "lan-only" || !reflect.DeepEqual(got.AllowedNetworks, routes) {
		t.Fatalf("initial status = %+v", got)
	}
	if err := manager.Set(context.Background(), "sync-anywhere"); err != nil {
		t.Fatal(err)
	}
	state, err := readNetworkProfileState(manager.statePath)
	if err != nil || state.Profile != "sync-anywhere" {
		t.Fatalf("persisted state = %+v, %v", state, err)
	}
	if changed, err := manager.RefreshIfChanged(context.Background()); err != nil || changed {
		t.Fatalf("anywhere refresh = %v, %v", changed, err)
	}
	if err := manager.Set(context.Background(), "lan-only"); err != nil {
		t.Fatal(err)
	}
	routes = []string{"10.20.0.0/16", "2001:db8::/64"}
	if changed, err := manager.RefreshIfChanged(context.Background()); err != nil || !changed {
		t.Fatalf("LAN route refresh = %v, %v", changed, err)
	}
	if got := manager.Status(); !reflect.DeepEqual(got.AllowedNetworks, routes) {
		t.Fatalf("refreshed status = %+v", got)
	}
	if len(upstream.requests) != 4 {
		t.Fatalf("requests = %+v", upstream.requests)
	}
}
