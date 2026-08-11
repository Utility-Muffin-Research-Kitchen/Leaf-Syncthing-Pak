package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/cards"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/leaf"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/life1"
	syncthingconfig "github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/uicontrol"
)

type fakeUpstream struct {
	done         chan error
	shutdownCall bool
}

type fakeB3Upstream struct {
	*fakeUpstream
	devices     []string
	folders     map[string]syncthingconfig.ConfiguredFolder
	paused      map[string]bool
	pauseCalls  []bool
	rescanCalls int
	offers      []syncthingconfig.UIFolderOffer
}

func newFakeB3Upstream() *fakeB3Upstream {
	return &fakeB3Upstream{
		fakeUpstream: &fakeUpstream{done: make(chan error)},
		devices: []string{
			"AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH",
			"IIIIIII-JJJJJJJ-KKKKKKK-LLLLLLL-MMMMMMM-NNNNNNN-OOOOOOO-PPPPPPP",
		},
		folders: make(map[string]syncthingconfig.ConfiguredFolder), paused: make(map[string]bool),
	}
}

func (upstream *fakeB3Upstream) ReadUIStatus(_ context.Context, folders []syncthingconfig.ConfiguredFolder, _ string) (syncthingconfig.UIStatus, error) {
	status := syncthingconfig.UIStatus{
		Folders:      make(map[string]syncthingconfig.UIFolderStatus),
		FolderOffers: append([]syncthingconfig.UIFolderOffer(nil), upstream.offers...),
	}
	for index, deviceID := range upstream.devices[1:] {
		status.Peers = append(status.Peers, syncthingconfig.UIPeerStatus{
			ID: deviceID, Name: fmt.Sprintf("Peer %d", index+1), State: "offline", Connection: "none",
		})
	}
	for _, folder := range folders {
		state := "idle"
		if upstream.paused[folder.ID] {
			state = "paused"
		}
		status.Folders[folder.ID] = syncthingconfig.UIFolderStatus{
			ID: folder.ID, State: state, LocalBytes: 9, GlobalBytes: 11, LocalItems: 1, GlobalItems: 2,
		}
	}
	return status, nil
}

func (upstream *fakeB3Upstream) ReadGameCheckStatus(context.Context, []syncthingconfig.ConfiguredFolder, string) (syncthingconfig.GameCheckStatus, error) {
	return syncthingconfig.GameCheckStatus{Current: true}, nil
}

func (upstream *fakeB3Upstream) SetFolderPaused(_ context.Context, folderID string, paused bool) error {
	upstream.paused[folderID] = paused
	upstream.pauseCalls = append(upstream.pauseCalls, paused)
	return nil
}

func (upstream *fakeB3Upstream) RescanFolder(context.Context, string) error {
	upstream.rescanCalls++
	return nil
}

func (upstream *fakeB3Upstream) RenameFolder(_ context.Context, folderID, label string) error {
	folder := upstream.folders[folderID]
	folder.Label = label
	upstream.folders[folderID] = folder
	return nil
}

func (upstream *fakeB3Upstream) AddPeer(context.Context, string, string, []string) error {
	return nil
}

func (upstream *fakeB3Upstream) RenamePeer(context.Context, string, string) error { return nil }

func (upstream *fakeB3Upstream) RemovePeer(_ context.Context, deviceID, _ string) error {
	for index, configured := range upstream.devices {
		if configured == deviceID {
			upstream.devices = append(upstream.devices[:index], upstream.devices[index+1:]...)
			break
		}
	}
	return nil
}

func (upstream *fakeB3Upstream) ConfiguredFolderDevices(context.Context, string) ([]string, error) {
	return append([]string(nil), upstream.devices...), nil
}

func (upstream *fakeB3Upstream) AddManagedFolder(_ context.Context, folder syncthingconfig.ConfiguredFolder) error {
	upstream.folders[folder.ID] = folder
	upstream.paused[folder.ID] = true
	remaining := upstream.offers[:0]
	for _, offer := range upstream.offers {
		if offer.FolderID != folder.ID {
			remaining = append(remaining, offer)
		}
	}
	upstream.offers = remaining
	return nil
}

func (upstream *fakeB3Upstream) SetManagedFolderType(_ context.Context, folder syncthingconfig.ConfiguredFolder) error {
	upstream.folders[folder.ID] = folder
	upstream.paused[folder.ID] = true
	return nil
}

func (upstream *fakeB3Upstream) RelocateManagedFolder(_ context.Context, folder syncthingconfig.ConfiguredFolder) error {
	upstream.folders[folder.ID] = folder
	return nil
}

func (upstream *fakeB3Upstream) SetManagedFolderDevices(_ context.Context, folder syncthingconfig.ConfiguredFolder) error {
	upstream.folders[folder.ID] = folder
	upstream.paused[folder.ID] = true
	return nil
}

func (upstream *fakeB3Upstream) RemoveManagedFolder(_ context.Context, folderID string) error {
	delete(upstream.folders, folderID)
	delete(upstream.paused, folderID)
	return nil
}

func (upstream *fakeUpstream) Done() <-chan error { return upstream.done }
func (upstream *fakeUpstream) Shutdown(context.Context) error {
	upstream.shutdownCall = true
	return nil
}

func TestRunRejectsUnexpectedGameStartThenShutsDown(t *testing.T) {
	config := testConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan life1.Event, 1)
	events <- life1.Event{Version: 1, Name: "game.start", LaunchID: "launch"}
	rejected := make(chan string, 1)
	lifecycle := &fakeLifecycle{
		next: func(ctx context.Context) (life1.Event, error) {
			select {
			case event := <-events:
				return event, nil
			case <-ctx.Done():
				return life1.Event{}, ctx.Err()
			}
		},
		reject: func(launchID, reason string) error {
			info, err := os.Lstat(config.ControlSocket)
			if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
				t.Fatalf("control socket mode = %v, error=%v", info, err)
			}
			if reason != "pause-unavailable" {
				t.Fatalf("game.start reason = %s", reason)
			}
			rejected <- launchID
			cancel()
			return nil
		},
	}
	upstream := &fakeUpstream{done: make(chan error)}
	runner := testServiceRunner(config, lifecycle, upstream)
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got := <-rejected; got != "launch" {
		t.Fatalf("rejected launch = %s", got)
	}
	if !upstream.shutdownCall {
		t.Fatal("controller did not shut down upstream")
	}
	if _, err := os.Lstat(config.ControlSocket); !os.IsNotExist(err) {
		t.Fatalf("control socket remained after controller exit: %v", err)
	}
}

func TestRunRejectsGameCheckWhenCardRefreshFails(t *testing.T) {
	config := testConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan life1.Event, 1)
	events <- life1.Event{Version: 1, Name: "game.check", LaunchID: "launch", SourceID: "primary"}
	rejected := make(chan string, 1)
	lifecycle := &fakeLifecycle{
		next: func(ctx context.Context) (life1.Event, error) {
			select {
			case event := <-events:
				return event, nil
			case <-ctx.Done():
				return life1.Event{}, ctx.Err()
			}
		},
		reject: func(_ string, reason string) error {
			rejected <- reason
			cancel()
			return nil
		},
	}
	upstream := &fakeUpstream{done: make(chan error)}
	runner := testServiceRunner(config, lifecycle, upstream)
	loads := 0
	runner.LoadCards = func(leaf.SourceList, string) ([]cards.Card, error) {
		loads++
		if loads > 1 {
			return nil, errors.New("inventory unavailable")
		}
		return nil, nil
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if reason := <-rejected; reason != "unsafe-card-binding" || loads != 2 {
		t.Fatalf("rejection = %s, inventory loads = %d", reason, loads)
	}
}

func TestReconcileGameStateIgnoresStaleFinish(t *testing.T) {
	current := life1.GameState{Active: true, LaunchID: "current", SourceID: "primary"}
	if got := reconcileGameState(current, life1.Event{Name: "game.cancel", LaunchID: "current"}); got != current {
		t.Fatalf("cancel changed authoritative game state: %+v", got)
	}
	if got := reconcileGameState(current, life1.Event{Name: "game.finish", LaunchID: "old"}); got != current {
		t.Fatalf("stale finish changed game state: %+v", got)
	}
	if got := reconcileGameState(current, life1.Event{Name: "game.finish", LaunchID: "current"}); got.Active {
		t.Fatalf("current finish did not clear game state: %+v", got)
	}
}

func TestRunReconnectsLifecycle(t *testing.T) {
	config := testConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstLifecycle := &fakeLifecycle{
		next: func(context.Context) (life1.Event, error) {
			return life1.Event{}, life1.ErrClosed
		},
	}
	secondLifecycle := &fakeLifecycle{}
	upstream := &fakeUpstream{done: make(chan error)}
	runner := testServiceRunner(config, firstLifecycle, upstream)
	connectCalls := 0
	runner.Connect = func(context.Context, life1.Config) (Lifecycle, life1.GameState, error) {
		connectCalls++
		if connectCalls == 1 {
			return firstLifecycle, life1.GameState{}, nil
		}
		cancel()
		return secondLifecycle, life1.GameState{}, nil
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if connectCalls != 2 || !firstLifecycle.closed {
		t.Fatalf("reconnect calls = %d, first closed = %v", connectCalls, firstLifecycle.closed)
	}
}

func TestRunReconnectStopsUpstreamForActiveGame(t *testing.T) {
	config := testConfig(t)
	config.Mode = life1.ModeStop
	firstLifecycle := &fakeLifecycle{
		next: func(context.Context) (life1.Event, error) {
			return life1.Event{}, life1.ErrClosed
		},
	}
	secondLifecycle := &fakeLifecycle{}
	upstream := &fakeUpstream{done: make(chan error)}
	runner := testServiceRunner(config, firstLifecycle, upstream)
	connectCalls := 0
	runner.Connect = func(context.Context, life1.Config) (Lifecycle, life1.GameState, error) {
		connectCalls++
		if connectCalls == 1 {
			return firstLifecycle, life1.GameState{}, nil
		}
		return secondLifecycle, life1.GameState{Active: true, LaunchID: "active"}, nil
	}
	if err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if connectCalls != 2 || !upstream.shutdownCall {
		t.Fatalf("reconnect calls = %d, upstream stopped = %v", connectCalls, upstream.shutdownCall)
	}
}

func TestRunReconnectFailureStopsUpstream(t *testing.T) {
	config := testConfig(t)
	firstLifecycle := &fakeLifecycle{
		next: func(context.Context) (life1.Event, error) {
			return life1.Event{}, life1.ErrClosed
		},
	}
	upstream := &fakeUpstream{done: make(chan error)}
	runner := testServiceRunner(config, firstLifecycle, upstream)
	connectCalls := 0
	runner.Connect = func(context.Context, life1.Config) (Lifecycle, life1.GameState, error) {
		connectCalls++
		if connectCalls == 1 {
			return firstLifecycle, life1.GameState{}, nil
		}
		return nil, life1.GameState{}, errors.New("reconnect rejected")
	}
	if err := runner.Run(context.Background()); err == nil {
		t.Fatal("reconnect failure was accepted")
	}
	if connectCalls != 2 || !upstream.shutdownCall {
		t.Fatalf("reconnect calls = %d, upstream stopped = %v", connectCalls, upstream.shutdownCall)
	}
}

func TestRunEnrollsCardThroughControlSocket(t *testing.T) {
	config := testConfig(t)
	lifecycle := &fakeLifecycle{}
	upstream := &fakeUpstream{done: make(chan error)}
	runner := testServiceRunner(config, lifecycle, upstream)
	enrolled := false
	runner.EnrollCard = func(source leaf.Source) (cards.Identity, bool, error) {
		if source.ID != "primary" {
			t.Fatalf("enrolled source = %s", source.ID)
		}
		enrolled = true
		return cards.Identity{Version: 1, ID: "00112233445566778899aabbccddeeff"}, true, nil
	}
	runner.LoadCards = func(sources leaf.SourceList, _ string) ([]cards.Card, error) {
		card := cards.Card{Source: sources[0], State: cards.StateUnenrolled, Present: true, Writable: true}
		if enrolled {
			card.State = cards.StateEnrolled
			card.Identity = cards.Identity{Version: 1, ID: "00112233445566778899aabbccddeeff"}
		}
		return []cards.Card{card}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Lstat(config.ControlSocket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("control socket did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	connection, err := net.Dial("unix", config.ControlSocket)
	if err != nil {
		t.Fatal(err)
	}
	request := json.RawMessage(`{"v":1,"id":"enroll","op":"card.enroll","args":{"source_id":"primary"}}`)
	if err := life1.Write(connection, request); err != nil {
		t.Fatal(err)
	}
	payload, err := life1.Read(connection)
	_ = connection.Close()
	if err != nil {
		t.Fatal(err)
	}
	var response uicontrol.Response
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.Result == nil || len(response.Result.Cards) != 1 ||
		response.Result.Cards[0].State != "enrolled" || !enrolled {
		t.Fatalf("enrollment response = %+v, enrolled=%v", response, enrolled)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunFolderOnboardingFirstSyncAndSendOnlyTransition(t *testing.T) {
	config := testConfig(t)
	saves := filepath.Join(config.Sources[0].Root, "Saves")
	states := filepath.Join(config.Sources[0].Root, "States")
	if err := os.MkdirAll(saves, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(saves, "slot.sav"), []byte("save-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	config.Sources[0].SavesPath = saves
	config.Sources[0].StatesPath = states
	card := cards.Card{
		Source: config.Sources[0], Identity: cards.Identity{Version: 1, ID: "00112233445566778899aabbccddeeff"},
		State: cards.StateEnrolled, Present: true, Writable: true,
	}
	var enrolled atomic.Bool
	events := make(chan life1.Event, 1)
	checkStopped := make(chan string, 1)
	checkRejected := make(chan string, 1)
	lifecycle := &fakeLifecycle{
		next: func(ctx context.Context) (life1.Event, error) {
			select {
			case event := <-events:
				return event, nil
			case <-ctx.Done():
				return life1.Event{}, ctx.Err()
			}
		},
		stop: func(launchID string) error {
			checkStopped <- launchID
			return nil
		},
		reject: func(_ string, reason string) error {
			checkRejected <- reason
			return nil
		},
	}
	upstream := newFakeB3Upstream()
	upstream.devices = append(upstream.devices,
		"CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH-IIIIIII-JJJJJJJ")
	runner := Runner{
		Config: config,
		Connect: func(context.Context, life1.Config) (Lifecycle, life1.GameState, error) {
			return lifecycle, life1.GameState{}, nil
		},
		Recover: func(string, syncthingconfig.SyncFilesystemFunc) (syncthingconfig.RecoveryResult, error) {
			return syncthingconfig.RecoveryResult{State: syncthingconfig.RecoveryClean}, nil
		},
		EnsureIdentity: func(ctx context.Context, options syncthingconfig.IdentityOptions, recovery syncthingconfig.RecoveryResult) (syncthingconfig.Identity, error) {
			identity, err := successfulIdentity(ctx, options, recovery)
			identity.DeviceID = onboardingSelf
			return identity, err
		},
		ApplyPause: successfulPause,
		StartProcess: func(context.Context, syncthingconfig.ProcessOptions) (UpstreamProcess, error) {
			return upstream, nil
		},
		EnrollCard: func(leaf.Source) (cards.Identity, bool, error) {
			enrolled.Store(true)
			return card.Identity, true, nil
		},
		LoadCards: func(leaf.SourceList, string) ([]cards.Card, error) {
			if !enrolled.Load() {
				unenrolled := card
				unenrolled.Identity = cards.Identity{}
				unenrolled.State = cards.StateUnenrolled
				return []cards.Card{unenrolled}, nil
			}
			return []cards.Card{card}, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	waitForControlSocket(t, config.ControlSocket)
	enroll := sendUIControlRequest(t, config.ControlSocket,
		`{"v":1,"id":"enroll","op":"card.enroll","args":{"source_id":"primary"}}`)
	if !enroll.OK || enroll.Result == nil || len(enroll.Result.Cards) != 1 ||
		enroll.Result.Cards[0].State != "enrolled" {
		t.Fatalf("card enrollment = %+v", enroll)
	}

	plan := sendUIControlRequest(t, config.ControlSocket,
		`{"v":1,"id":"plan","op":"folder.onboard.plan","args":{"source_id":"primary","kind":"saves","folder_type":"sendreceive","device_ids":["IIIIIII-JJJJJJJ-KKKKKKK-LLLLLLL-MMMMMMM-NNNNNNN-OOOOOOO-PPPPPPP"]}}`)
	if !plan.OK || plan.Result == nil || plan.Result.Onboarding == nil || plan.Result.Onboarding.FileCount != 1 || !plan.Result.Onboarding.SnapshotPossible {
		t.Fatalf("onboarding plan = %+v", plan)
	}
	planID := plan.Result.Onboarding.PlanID
	create := sendUIControlRequest(t, config.ControlSocket, fmt.Sprintf(
		`{"v":1,"id":"create","op":"folder.onboard.create","args":{"plan_id":"%s","confirmed":true,"states_warning_acknowledged":false,"manual_edit_warning_acknowledged":true}}`, planID))
	if !create.OK || create.Result == nil || len(create.Result.Folders) != 1 ||
		create.Result.Folders[0].FirstSyncState != "required" || !create.Result.Folders[0].Paused {
		t.Fatalf("onboarding create = %+v", create)
	}
	folderID := create.Result.Folders[0].ID
	if folder := upstream.folders[folderID]; len(folder.Devices) != 2 ||
		folder.Devices[1] != "IIIIIII-JJJJJJJ-KKKKKKK-LLLLLLL-MMMMMMM-NNNNNNN-OOOOOOO-PPPPPPP" {
		t.Fatalf("explicit folder peer selection = %v", folder.Devices)
	}
	prepare := sendUIControlRequest(t, config.ControlSocket, fmt.Sprintf(
		`{"v":1,"id":"prepare","op":"folder.first-sync.prepare","args":{"folder_id":"%s","confirmed":true,"snapshot_limit_acknowledged":true}}`, folderID))
	if !prepare.OK || prepare.Result == nil || prepare.Result.Folders[0].FirstSyncState != "ready" ||
		prepare.Result.Folders[0].SnapshotFiles != 1 || prepare.Result.Folders[0].LocalItems != 1 || prepare.Result.Folders[0].GlobalItems != 2 {
		t.Fatalf("first-sync prepare = %+v", prepare)
	}
	start := sendUIControlRequest(t, config.ControlSocket, fmt.Sprintf(
		`{"v":1,"id":"start","op":"folder.first-sync.start","args":{"folder_id":"%s","confirmed":true,"hub_versioning_acknowledged":true}}`, folderID))
	if !start.OK || start.Result == nil || start.Result.Folders[0].FirstSyncState != "complete" || start.Result.Folders[0].Paused {
		t.Fatalf("first-sync start = %+v", start)
	}
	blockedRemoval := sendUIControlRequest(t, config.ControlSocket,
		`{"v":1,"id":"remove-shared-peer","op":"device.remove","args":{"device_id":"`+onboardingPeer+`","confirmed":true}}`)
	if blockedRemoval.OK || blockedRemoval.Error == nil || !containsDevice(upstream.devices, onboardingPeer) {
		t.Fatalf("shared peer removal = %+v, devices=%v", blockedRemoval, upstream.devices)
	}
	share := sendUIControlRequest(t, config.ControlSocket, fmt.Sprintf(
		`{"v":1,"id":"share","op":"folder.share","args":{"folder_id":"%s","device_id":"%s","confirmed":true}}`, folderID, onboardingOtherPeer))
	if !share.OK || share.Result == nil || len(share.Result.Folders[0].DeviceIDs) != 3 || share.Result.Folders[0].Paused {
		t.Fatalf("folder share = %+v", share)
	}
	unshare := sendUIControlRequest(t, config.ControlSocket, fmt.Sprintf(
		`{"v":1,"id":"unshare","op":"folder.unshare","args":{"folder_id":"%s","device_id":"%s","confirmed":true}}`, folderID, onboardingPeer))
	if !unshare.OK || unshare.Result == nil || len(unshare.Result.Folders[0].DeviceIDs) != 2 ||
		unshare.Result.Folders[0].DeviceIDs[1] != onboardingOtherPeer || unshare.Result.Folders[0].Paused {
		t.Fatalf("folder unshare = %+v", unshare)
	}
	localOnly := sendUIControlRequest(t, config.ControlSocket, fmt.Sprintf(
		`{"v":1,"id":"unshare-final","op":"folder.unshare","args":{"folder_id":"%s","device_id":"%s","confirmed":true}}`, folderID, onboardingOtherPeer))
	if !localOnly.OK || localOnly.Result == nil || len(localOnly.Result.Folders[0].DeviceIDs) != 1 ||
		localOnly.Result.Folders[0].DeviceIDs[0] != onboardingSelf || localOnly.Result.Folders[0].PeerCount != 0 ||
		localOnly.Result.Folders[0].Paused {
		t.Fatalf("folder final unshare = %+v", localOnly)
	}
	removed := sendUIControlRequest(t, config.ControlSocket,
		`{"v":1,"id":"remove-peer","op":"device.remove","args":{"device_id":"`+onboardingPeer+`","confirmed":true}}`)
	if !removed.OK || removed.Result == nil || containsDevice(upstream.devices, onboardingPeer) {
		t.Fatalf("peer removal = %+v, devices=%v", removed, upstream.devices)
	}
	if pending := folderControlsPathPendingDeviceRemoval(t, config); pending != "" {
		t.Fatalf("pending device removal = %q", pending)
	}
	records, _, _, _, _, err := readFolderControlState(filepath.Join(config.UserdataPath, leaf.AppStateName, "leaf", folderControlStateName))
	if err != nil {
		t.Fatal(err)
	}
	if record := records[folderID]; record.PendingMembership != "" || record.PendingDeviceID != "" {
		t.Fatalf("completed folder membership intent = %+v", record)
	}

	toSendOnly := sendUIControlRequest(t, config.ControlSocket, fmt.Sprintf(
		`{"v":1,"id":"sendonly","op":"folder.type.set","args":{"folder_id":"%s","folder_type":"sendonly","confirmed":true}}`, folderID))
	if !toSendOnly.OK || toSendOnly.Result.Folders[0].Type != "sendonly" ||
		toSendOnly.Result.Folders[0].FirstSyncState != "required" || !toSendOnly.Result.Folders[0].Paused {
		t.Fatalf("send-only transition = %+v", toSendOnly)
	}
	startSendOnly := sendUIControlRequest(t, config.ControlSocket, fmt.Sprintf(
		`{"v":1,"id":"start-sendonly","op":"folder.first-sync.start","args":{"folder_id":"%s","confirmed":true,"hub_versioning_acknowledged":true}}`, folderID))
	if !startSendOnly.OK || startSendOnly.Result.Folders[0].FirstSyncState != "complete" || startSendOnly.Result.Folders[0].Paused {
		t.Fatalf("send-only start = %+v", startSendOnly)
	}

	toReceive := sendUIControlRequest(t, config.ControlSocket, fmt.Sprintf(
		`{"v":1,"id":"receive","op":"folder.type.set","args":{"folder_id":"%s","folder_type":"sendreceive","confirmed":true}}`, folderID))
	if !toReceive.OK || toReceive.Result.Folders[0].FirstSyncState != "required" || !toReceive.Result.Folders[0].Paused {
		t.Fatalf("receive transition = %+v", toReceive)
	}
	unsafeStart := sendUIControlRequest(t, config.ControlSocket, fmt.Sprintf(
		`{"v":1,"id":"unsafe-start","op":"folder.first-sync.start","args":{"folder_id":"%s","confirmed":true,"hub_versioning_acknowledged":true}}`, folderID))
	if unsafeStart.OK || unsafeStart.Error == nil || unsafeStart.Error.Code != "operation-failed" {
		t.Fatalf("receive transition reused old snapshot = %+v", unsafeStart)
	}
	prepareAgain := sendUIControlRequest(t, config.ControlSocket, fmt.Sprintf(
		`{"v":1,"id":"prepare-again","op":"folder.first-sync.prepare","args":{"folder_id":"%s","confirmed":true,"snapshot_limit_acknowledged":true}}`, folderID))
	if !prepareAgain.OK || prepareAgain.Result.Folders[0].FirstSyncState != "ready" {
		t.Fatalf("receive re-prepare = %+v", prepareAgain)
	}
	startAgain := sendUIControlRequest(t, config.ControlSocket, fmt.Sprintf(
		`{"v":1,"id":"start-again","op":"folder.first-sync.start","args":{"folder_id":"%s","confirmed":true,"hub_versioning_acknowledged":true}}`, folderID))
	if !startAgain.OK || startAgain.Result.Folders[0].FirstSyncState != "complete" || startAgain.Result.Folders[0].Paused {
		t.Fatalf("receive restart = %+v", startAgain)
	}
	events <- life1.Event{
		Version: 1, Name: "game.check", LaunchID: "after-onboarding", SourceID: "primary",
		SavesPath: saves, StatesPath: states,
	}
	select {
	case launchID := <-checkStopped:
		if launchID != "after-onboarding" {
			t.Fatalf("stopped launch = %s", launchID)
		}
	case reason := <-checkRejected:
		t.Fatalf("game check after live enrollment was rejected: %s", reason)
	case <-time.After(2 * time.Second):
		t.Fatal("game check after live enrollment did not finish")
	}
	if len(upstream.pauseCalls) < 5 || upstream.pauseCalls[len(upstream.pauseCalls)-1] {
		t.Fatalf("upstream pause transitions = %v", upstream.pauseCalls)
	}
	stopped := sendUIControlRequest(t, config.ControlSocket, fmt.Sprintf(
		`{"v":1,"id":"stop-local","op":"folder.stop","args":{"folder_id":"%s","confirmed":true}}`, folderID))
	if !stopped.OK || stopped.Result == nil || len(stopped.Result.Folders) != 0 || stopped.Result.Storage == nil || stopped.Result.Storage.SnapshotCount == 0 {
		t.Fatalf("local folder stop = %+v", stopped)
	}
	if _, present := upstream.folders[folderID]; present {
		t.Fatalf("stopped upstream folder = %#v", upstream.folders[folderID])
	}
	if payload, err := os.ReadFile(filepath.Join(saves, "slot.sav")); err != nil || string(payload) != "save-data" {
		t.Fatalf("live save after local stop = %q, %v", payload, err)
	}
	_, markerName, err := cards.BindingNames(card.Identity.ID, "saves")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(saves, markerName)); !os.IsNotExist(err) {
		t.Fatalf("Leaf marker after local stop = %v", err)
	}
	records, _, _, _, _, err = readFolderControlState(filepath.Join(config.UserdataPath, leaf.AppStateName, "leaf", folderControlStateName))
	if err != nil || len(records) != 0 {
		t.Fatalf("folder controls after local stop = %#v, %v", records, err)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func containsDevice(devices []string, deviceID string) bool {
	for _, configured := range devices {
		if configured == deviceID {
			return true
		}
	}
	return false
}

func folderControlsPathPendingDeviceRemoval(t *testing.T, config Config) string {
	t.Helper()
	_, _, pending, _, _, err := readFolderControlState(filepath.Join(config.UserdataPath, leaf.AppStateName, "leaf", folderControlStateName))
	if err != nil {
		t.Fatal(err)
	}
	return pending
}

func TestRunPlansAndAcceptsStandardFolderOffer(t *testing.T) {
	config := testConfig(t)
	saves := filepath.Join(config.Sources[0].Root, "Saves")
	if err := os.MkdirAll(saves, 0o700); err != nil {
		t.Fatal(err)
	}
	config.Sources[0].SavesPath = saves
	config.Sources[0].StatesPath = filepath.Join(config.Sources[0].Root, "States")
	card := cards.Card{
		Source: config.Sources[0], Identity: cards.Identity{Version: 1, ID: "00112233445566778899aabbccddeeff"},
		State: cards.StateEnrolled, Present: true, Writable: true,
	}
	upstream := newFakeB3Upstream()
	upstream.devices = append(upstream.devices, "CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH-IIIIIII-JJJJJJJ")
	upstream.offers = []syncthingconfig.UIFolderOffer{{
		FolderID: "retro-saves", Label: "Retro Saves", DeviceID: onboardingPeer, DeviceName: "Laptop",
		OfferedAt: "2026-08-09T12:34:56Z",
	}}
	runner := Runner{
		Config: config,
		Connect: func(context.Context, life1.Config) (Lifecycle, life1.GameState, error) {
			return &fakeLifecycle{}, life1.GameState{}, nil
		},
		Recover: func(string, syncthingconfig.SyncFilesystemFunc) (syncthingconfig.RecoveryResult, error) {
			return syncthingconfig.RecoveryResult{State: syncthingconfig.RecoveryClean}, nil
		},
		EnsureIdentity: func(ctx context.Context, options syncthingconfig.IdentityOptions, recovery syncthingconfig.RecoveryResult) (syncthingconfig.Identity, error) {
			identity, err := successfulIdentity(ctx, options, recovery)
			identity.DeviceID = onboardingSelf
			return identity, err
		},
		ApplyPause: successfulPause,
		StartProcess: func(context.Context, syncthingconfig.ProcessOptions) (UpstreamProcess, error) {
			return upstream, nil
		},
		LoadCards: func(leaf.SourceList, string) ([]cards.Card, error) {
			return []cards.Card{card}, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	waitForControlSocket(t, config.ControlSocket)

	ignored := sendUIControlRequest(t, config.ControlSocket,
		`{"v":1,"id":"offer-ignore","op":"folder.offer.ignore","args":{"folder_id":"retro-saves","device_id":"`+onboardingPeer+`","confirmed":true}}`)
	if !ignored.OK || ignored.Result == nil || len(ignored.Result.FolderOffers) != 1 || !ignored.Result.FolderOffers[0].Ignored {
		t.Fatalf("ignored offer = %+v", ignored)
	}
	blocked := sendUIControlRequest(t, config.ControlSocket,
		`{"v":1,"id":"offer-plan-blocked","op":"folder.offer.plan","args":{"folder_id":"retro-saves","device_id":"`+onboardingPeer+`","source_id":"primary","kind":"saves","folder_type":"sendreceive"}}`)
	if blocked.OK || blocked.Error == nil || blocked.Error.Message != "Restore this ignored folder offer before reviewing it" {
		t.Fatalf("ignored offer plan = %+v", blocked)
	}
	restored := sendUIControlRequest(t, config.ControlSocket,
		`{"v":1,"id":"offer-restore","op":"folder.offer.restore","args":{"folder_id":"retro-saves","device_id":"`+onboardingPeer+`","confirmed":true}}`)
	if !restored.OK || restored.Result == nil || len(restored.Result.FolderOffers) != 1 || restored.Result.FolderOffers[0].Ignored {
		t.Fatalf("restored offer = %+v", restored)
	}

	plan := sendUIControlRequest(t, config.ControlSocket,
		`{"v":1,"id":"offer-plan","op":"folder.offer.plan","args":{"folder_id":"retro-saves","device_id":"`+onboardingPeer+`","source_id":"primary","kind":"saves","folder_type":"sendreceive"}}`)
	if !plan.OK || plan.Result == nil || plan.Result.Onboarding == nil || !plan.Result.Onboarding.JoinExisting ||
		plan.Result.Onboarding.FolderID != "retro-saves" || plan.Result.Onboarding.OfferDeviceID != onboardingPeer {
		t.Fatalf("offer plan = %+v", plan)
	}
	create := sendUIControlRequest(t, config.ControlSocket, fmt.Sprintf(
		`{"v":1,"id":"offer-create","op":"folder.onboard.create","args":{"plan_id":"%s","confirmed":true,"states_warning_acknowledged":false,"manual_edit_warning_acknowledged":true}}`,
		plan.Result.Onboarding.PlanID))
	if !create.OK || create.Result == nil || len(create.Result.Folders) != 1 || create.Result.Folders[0].ID != "retro-saves" ||
		create.Result.Folders[0].FirstSyncState != "required" || len(create.Result.FolderOffers) != 0 {
		t.Fatalf("accepted offer = %+v", create)
	}
	created := upstream.folders["retro-saves"]
	if len(created.Devices) != 2 || created.Devices[0] != onboardingSelf || created.Devices[1] != onboardingPeer {
		t.Fatalf("accepted offer devices = %+v", created.Devices)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunReturnsUnexpectedUpstreamExit(t *testing.T) {
	config := testConfig(t)
	lifecycle := &fakeLifecycle{}
	upstream := &fakeUpstream{done: make(chan error, 1)}
	upstream.done <- errors.New("fixture exit")
	close(upstream.done)
	runner := testServiceRunner(config, lifecycle, upstream)
	if err := runner.Run(context.Background()); err == nil {
		t.Fatal("unexpected upstream exit was accepted")
	}
	if upstream.shutdownCall {
		t.Fatal("controller attempted an in-generation restart/stop after upstream exit")
	}
}

func TestRunServesReportedConflictWithoutSecondUpstream(t *testing.T) {
	config := testConfig(t)
	runner := testServiceRunner(config, &fakeLifecycle{}, &fakeUpstream{done: make(chan error)})
	runner.StartProcess = func(context.Context, syncthingconfig.ProcessOptions) (UpstreamProcess, error) {
		return nil, syncthingconfig.Conflict{ProcessIDs: []int{42}, ConventionalPort: true}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Lstat(config.ControlSocket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("conflict control socket did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	connection, err := net.Dial("unix", config.ControlSocket)
	if err != nil {
		t.Fatal(err)
	}
	if err := life1.Write(connection, json.RawMessage(`{"v":1,"id":"conflict","op":"status.get","args":{}}`)); err != nil {
		t.Fatal(err)
	}
	payload, err := life1.Read(connection)
	_ = connection.Close()
	if err != nil {
		t.Fatal(err)
	}
	var response uicontrol.Response
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.Result == nil || response.Result.Upstream.State != "conflict" ||
		len(response.Result.Issues) != 1 || response.Result.Issues[0].Code != "foreign-syncthing" {
		t.Fatalf("conflict response = %+v", response)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func waitForControlSocket(t *testing.T, socket string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Lstat(socket); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("control socket did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func sendUIControlRequest(t *testing.T, socket, request string) uicontrol.Response {
	t.Helper()
	connection, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := life1.Write(connection, json.RawMessage(request)); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	payload, err := life1.Read(connection)
	_ = connection.Close()
	if err != nil {
		t.Fatal(err)
	}
	var response uicontrol.Response
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func testServiceRunner(config Config, lifecycle *fakeLifecycle, upstream *fakeUpstream) Runner {
	return Runner{
		Config: config,
		Connect: func(context.Context, life1.Config) (Lifecycle, life1.GameState, error) {
			return lifecycle, life1.GameState{}, nil
		},
		Recover: func(string, syncthingconfig.SyncFilesystemFunc) (syncthingconfig.RecoveryResult, error) {
			return syncthingconfig.RecoveryResult{State: syncthingconfig.RecoveryClean}, nil
		},
		EnsureIdentity: successfulIdentity,
		ApplyPause:     successfulPause,
		StartProcess: func(context.Context, syncthingconfig.ProcessOptions) (UpstreamProcess, error) {
			return upstream, nil
		},
		LoadCards: func(leaf.SourceList, string) ([]cards.Card, error) { return nil, nil },
	}
}
