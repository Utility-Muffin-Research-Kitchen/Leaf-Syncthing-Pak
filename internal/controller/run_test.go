package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
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
