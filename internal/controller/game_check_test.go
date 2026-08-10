package controller

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/cards"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/leaf"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/life1"
	syncthingconfig "github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
)

type fakeGameCheckUpstream struct {
	status syncthingconfig.GameCheckStatus
	err    error
}

func (upstream fakeGameCheckUpstream) ReadGameCheckStatus(context.Context, []syncthingconfig.ConfiguredFolder, string) (syncthingconfig.GameCheckStatus, error) {
	return upstream.status, upstream.err
}

func TestFoldersForGameCheckUsesPhysicalCardBinding(t *testing.T) {
	cardID := "00112233445566778899aabbccddeeff"
	root := t.TempDir()
	source := leaf.Source{ID: "secondary_sd", Root: root, SavesPath: filepath.Join(root, "Saves"), StatesPath: filepath.Join(root, "States")}
	marker := ".leaf-saves-marker"
	if err := os.MkdirAll(filepath.Join(source.SavesPath, marker), 0o700); err != nil {
		t.Fatal(err)
	}
	inventory := []cards.Card{{
		Source: source, Identity: cards.Identity{Version: 1, ID: cardID},
		State: cards.StateEnrolled, Present: true, Writable: true,
	}}
	folders := []syncthingconfig.ConfiguredFolder{{
		ID: "shared-saves", Kind: "saves", Path: source.SavesPath,
		Type: "sendonly", MarkerName: marker,
	}}
	selected, err := foldersForGameCheck(life1.Event{
		SourceID: source.ID, SavesPath: source.SavesPath, StatesPath: source.StatesPath,
	}, inventory, folders, map[string]folderControlRecord{
		"shared-saves": {CardID: cardID, Kind: "saves", MarkerName: marker},
	})
	if err != nil || len(selected) != 1 || selected[0].ID != "shared-saves" {
		t.Fatalf("selected = %+v, %v", selected, err)
	}
	if _, err := foldersForGameCheck(life1.Event{
		SourceID: source.ID, SavesPath: "/other/Saves", StatesPath: source.StatesPath,
	}, inventory, folders, map[string]folderControlRecord{}); err == nil {
		t.Fatal("mismatched launch path was accepted")
	}
	inventory[0].Writable = false
	if _, err := foldersForGameCheck(life1.Event{
		SourceID: source.ID, SavesPath: source.SavesPath, StatesPath: source.StatesPath,
	}, inventory, folders, map[string]folderControlRecord{}); err == nil {
		t.Fatal("read-only launch card was accepted")
	}
	inventory[0].Writable = true
	if err := os.Remove(filepath.Join(source.SavesPath, marker)); err != nil {
		t.Fatal(err)
	}
	if _, err := foldersForGameCheck(life1.Event{
		SourceID: source.ID, SavesPath: source.SavesPath, StatesPath: source.StatesPath,
	}, inventory, folders, map[string]folderControlRecord{
		"shared-saves": {CardID: cardID, Kind: "saves", MarkerName: marker},
	}); err == nil {
		t.Fatal("folder with a missing safety marker was accepted")
	}
}

func TestRunGameCheckSendsWaitingThenStop(t *testing.T) {
	stopped := make(chan string, 1)
	waiting := make(chan struct{}, 1)
	lifecycle := &fakeLifecycle{
		waiting: func(launchID string, items int, bytes int64) error {
			if launchID != "launch" || items != 3 || bytes != 49152 {
				t.Fatalf("waiting = %s, %d, %d", launchID, items, bytes)
			}
			waiting <- struct{}{}
			return nil
		},
		stop: func(launchID string) error {
			stopped <- launchID
			return nil
		},
	}
	checks := 0
	upstream := gameCheckFunc(func(context.Context, []syncthingconfig.ConfiguredFolder, string) (syncthingconfig.GameCheckStatus, error) {
		checks++
		if checks == 1 {
			return syncthingconfig.GameCheckStatus{PendingItems: 3, PendingBytes: 49152}, nil
		}
		return syncthingconfig.GameCheckStatus{Current: true}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	runGameCheck(ctx, lifecycle, upstream, nil, "SELF", life1.Event{LaunchID: "launch"}, 250, nil)
	select {
	case <-waiting:
	default:
		t.Fatal("waiting status was not sent")
	}
	if launchID := <-stopped; launchID != "launch" {
		t.Fatalf("stop launch id = %s", launchID)
	}
}

type gameCheckFunc func(context.Context, []syncthingconfig.ConfiguredFolder, string) (syncthingconfig.GameCheckStatus, error)

func (function gameCheckFunc) ReadGameCheckStatus(ctx context.Context, folders []syncthingconfig.ConfiguredFolder, selfDeviceID string) (syncthingconfig.GameCheckStatus, error) {
	return function(ctx, folders, selfDeviceID)
}
