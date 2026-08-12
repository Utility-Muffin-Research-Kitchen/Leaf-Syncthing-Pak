package controller

import (
	"context"
	"errors"
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

func TestFoldersForGameCheckUsesCardAtItsCurrentSource(t *testing.T) {
	cardID := "00112233445566778899aabbccddeeff"
	root := t.TempDir()
	source := leaf.Source{ID: "primary", Root: root, SavesPath: filepath.Join(root, "Saves"), StatesPath: filepath.Join(root, "States")}
	marker := ".leaf-states-marker"
	if err := os.MkdirAll(filepath.Join(source.StatesPath, marker), 0o700); err != nil {
		t.Fatal(err)
	}
	inventory := []cards.Card{{
		Source: source, Identity: cards.Identity{Version: 1, ID: cardID},
		State: cards.StateEnrolled, Present: true, Writable: true,
	}}
	folders := []syncthingconfig.ConfiguredFolder{{
		ID: "shared-states", Kind: "states", Path: source.StatesPath,
		Type: "sendonly", MarkerName: marker,
	}}
	selected, err := foldersForGameCheck(life1.Event{
		SourceID: source.ID, SavesPath: source.SavesPath, StatesPath: source.StatesPath,
	}, inventory, folders, map[string]folderControlRecord{
		"shared-states": {CardID: cardID, Kind: "states", MarkerName: marker},
	})
	if err != nil || len(selected) != 1 || selected[0].ID != "shared-states" {
		t.Fatalf("selected after card move = %+v, %v", selected, err)
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

func TestRunGameCheckAllowsSlowerFollowupAfterInitialWaiting(t *testing.T) {
	stopped := false
	rejected := false
	lifecycle := &fakeLifecycle{
		stop: func(string) error {
			stopped = true
			return nil
		},
		reject: func(string, string) error {
			rejected = true
			return nil
		},
	}
	checks := 0
	upstream := gameCheckFunc(func(ctx context.Context, _ []syncthingconfig.ConfiguredFolder, _ string) (syncthingconfig.GameCheckStatus, error) {
		checks++
		if checks == 1 {
			return syncthingconfig.GameCheckStatus{PendingItems: 1, PendingBytes: 4096}, nil
		}
		select {
		case <-time.After(300 * time.Millisecond):
			return syncthingconfig.GameCheckStatus{Current: true}, nil
		case <-ctx.Done():
			return syncthingconfig.GameCheckStatus{}, ctx.Err()
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	runGameCheck(ctx, lifecycle, upstream, nil, "SELF", life1.Event{LaunchID: "launch"}, 50, nil)
	if !stopped || rejected {
		t.Fatalf("stopped = %t, rejected = %t", stopped, rejected)
	}
}

func TestRunGameCheckRetriesTransientFollowupFailure(t *testing.T) {
	stopped := false
	rejected := false
	lifecycle := &fakeLifecycle{
		stop: func(string) error {
			stopped = true
			return nil
		},
		reject: func(string, string) error {
			rejected = true
			return nil
		},
	}
	checks := 0
	upstream := gameCheckFunc(func(context.Context, []syncthingconfig.ConfiguredFolder, string) (syncthingconfig.GameCheckStatus, error) {
		checks++
		switch checks {
		case 1:
			return syncthingconfig.GameCheckStatus{PendingItems: 1, PendingBytes: 4096}, nil
		case 2:
			return syncthingconfig.GameCheckStatus{}, errors.New("temporary status failure")
		default:
			return syncthingconfig.GameCheckStatus{Current: true}, nil
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	runGameCheck(ctx, lifecycle, upstream, nil, "SELF", life1.Event{LaunchID: "launch"}, 250, nil)
	if !stopped || rejected {
		t.Fatalf("stopped = %t, rejected = %t", stopped, rejected)
	}
}

// A launch landing while upstream's status endpoint is still coming up must
// not fail closed on the first read; the endpoint becomes usable moments later.
func TestRunGameCheckRetriesFirstStatusReadDuringStartup(t *testing.T) {
	stopped := false
	var rejection string
	lifecycle := &fakeLifecycle{
		stop: func(string) error {
			stopped = true
			return nil
		},
		reject: func(_ string, reason string) error {
			rejection = reason
			return nil
		},
	}
	checks := 0
	upstream := gameCheckFunc(func(context.Context, []syncthingconfig.ConfiguredFolder, string) (syncthingconfig.GameCheckStatus, error) {
		checks++
		if checks < 3 {
			return syncthingconfig.GameCheckStatus{}, errors.New("status endpoint not ready")
		}
		return syncthingconfig.GameCheckStatus{Current: true}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	runGameCheck(ctx, lifecycle, upstream, nil, "SELF", life1.Event{LaunchID: "launch"}, gameCheckAckMS, nil)
	if !stopped || rejection != "" {
		t.Fatalf("stopped = %t, rejection = %q after %d reads", stopped, rejection, checks)
	}
	if elapsed := time.Since(started); elapsed >= time.Duration(gameCheckAckMS)*time.Millisecond {
		t.Fatalf("initial reply took %s, want it inside ack_ms", elapsed)
	}
}

func TestRunGameCheckFirstReadTimeoutStillRepliesInsideAck(t *testing.T) {
	var rejection string
	lifecycle := &fakeLifecycle{reject: func(_ string, reason string) error {
		rejection = reason
		return nil
	}}
	upstream := gameCheckFunc(func(ctx context.Context, _ []syncthingconfig.ConfiguredFolder, _ string) (syncthingconfig.GameCheckStatus, error) {
		<-ctx.Done()
		return syncthingconfig.GameCheckStatus{}, ctx.Err()
	})
	started := time.Now()
	runGameCheck(context.Background(), lifecycle, upstream, nil, "SELF",
		life1.Event{LaunchID: "launch"}, 250, nil)
	if rejection != "sync-status-unavailable" {
		t.Fatalf("rejection = %q", rejection)
	}
	if elapsed := time.Since(started); elapsed >= 250*time.Millisecond {
		t.Fatalf("initial error took %s, want it inside ack_ms", elapsed)
	}
}

func TestRunGameCheckAcknowledgesSlowFirstProbeThenStops(t *testing.T) {
	stopped := false
	var rejection string
	var firstReply time.Duration
	started := time.Now()
	lifecycle := &fakeLifecycle{
		waiting: func(_ string, items int, bytes int64) error {
			firstReply = time.Since(started)
			if items != 1 || bytes != 0 {
				t.Fatalf("waiting = %d, %d", items, bytes)
			}
			return nil
		},
		stop: func(string) error {
			stopped = true
			return nil
		},
		reject: func(_ string, reason string) error {
			rejection = reason
			return nil
		},
	}
	checks := 0
	upstream := gameCheckFunc(func(ctx context.Context, _ []syncthingconfig.ConfiguredFolder, _ string) (syncthingconfig.GameCheckStatus, error) {
		checks++
		if checks == 1 {
			<-ctx.Done()
			return syncthingconfig.GameCheckStatus{}, ctx.Err()
		}
		return syncthingconfig.GameCheckStatus{Current: true}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	runGameCheck(ctx, lifecycle, upstream, nil, "SELF", life1.Event{LaunchID: "launch"}, gameCheckAckMS, nil)
	if !stopped || rejection != "" || checks != 2 {
		t.Fatalf("stopped = %t, rejection = %q, checks = %d", stopped, rejection, checks)
	}
	if firstReply <= 0 || firstReply >= time.Duration(gameCheckAckMS)*time.Millisecond {
		t.Fatalf("first reply took %s, want it inside ack_ms", firstReply)
	}
}

func TestRunGameCheckSlowProbeStillFailsClosedOnFollowupError(t *testing.T) {
	stopped := false
	waiting := false
	var rejection string
	checks := 0
	lifecycle := &fakeLifecycle{
		waiting: func(string, int, int64) error {
			waiting = true
			return nil
		},
		stop: func(string) error {
			stopped = true
			return nil
		},
		reject: func(_ string, reason string) error {
			rejection = reason
			return nil
		},
	}
	upstream := gameCheckFunc(func(ctx context.Context, _ []syncthingconfig.ConfiguredFolder, _ string) (syncthingconfig.GameCheckStatus, error) {
		checks++
		if checks == 1 {
			<-ctx.Done()
			return syncthingconfig.GameCheckStatus{}, ctx.Err()
		}
		return syncthingconfig.GameCheckStatus{}, errors.New("status endpoint down")
	})
	runGameCheck(context.Background(), lifecycle, upstream, nil, "SELF",
		life1.Event{LaunchID: "launch"}, gameCheckAckMS, nil)
	if !waiting || stopped || rejection != "sync-status-unavailable" || checks != 2 {
		t.Fatalf("waiting = %t, stopped = %t, rejection = %q, checks = %d",
			waiting, stopped, rejection, checks)
	}
}

// Retrying must not turn a genuinely unavailable status into a launch.
func TestRunGameCheckStillFailsClosedWhenStatusNeverArrives(t *testing.T) {
	stopped := false
	var rejection string
	lifecycle := &fakeLifecycle{
		stop: func(string) error {
			stopped = true
			return nil
		},
		reject: func(_ string, reason string) error {
			rejection = reason
			return nil
		},
	}
	upstream := gameCheckFunc(func(context.Context, []syncthingconfig.ConfiguredFolder, string) (syncthingconfig.GameCheckStatus, error) {
		return syncthingconfig.GameCheckStatus{}, errors.New("status endpoint down")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	runGameCheck(ctx, lifecycle, upstream, nil, "SELF", life1.Event{LaunchID: "launch"}, gameCheckAckMS, nil)
	if stopped || rejection != "sync-status-unavailable" {
		t.Fatalf("stopped = %t, rejection = %q", stopped, rejection)
	}
	if elapsed := time.Since(started); elapsed >= time.Duration(gameCheckAckMS)*time.Millisecond {
		t.Fatalf("fail-closed took %s, want it inside ack_ms", elapsed)
	}
}

type gameCheckFunc func(context.Context, []syncthingconfig.ConfiguredFolder, string) (syncthingconfig.GameCheckStatus, error)

func (function gameCheckFunc) ReadGameCheckStatus(ctx context.Context, folders []syncthingconfig.ConfiguredFolder, selfDeviceID string) (syncthingconfig.GameCheckStatus, error) {
	return function(ctx, folders, selfDeviceID)
}
