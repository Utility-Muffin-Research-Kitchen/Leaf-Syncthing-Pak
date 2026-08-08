package controller

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/life1"
	syncthingconfig "github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
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

func TestRunAnswersGameStartThenShutsDown(t *testing.T) {
	config := testConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan life1.Event, 1)
	events <- life1.Event{Version: 1, Name: "game.start", LaunchID: "launch"}
	ready := make(chan string, 1)
	lifecycle := &fakeLifecycle{
		next: func(ctx context.Context) (life1.Event, error) {
			select {
			case event := <-events:
				return event, nil
			case <-ctx.Done():
				return life1.Event{}, ctx.Err()
			}
		},
		ready: func(launchID string) error {
			info, err := os.Lstat(config.ControlSocket)
			if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
				t.Fatalf("control socket mode = %v, error=%v", info, err)
			}
			ready <- launchID
			cancel()
			return nil
		},
	}
	upstream := &fakeUpstream{done: make(chan error)}
	runner := testServiceRunner(config, lifecycle, upstream)
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got := <-ready; got != "launch" {
		t.Fatalf("ready launch = %s", got)
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
	if got := reconcileGameState(current, life1.Event{Name: "game.finish", LaunchID: "old"}); got != current {
		t.Fatalf("stale finish changed game state: %+v", got)
	}
	if got := reconcileGameState(current, life1.Event{Name: "game.finish", LaunchID: "current"}); got.Active {
		t.Fatalf("current finish did not clear game state: %+v", got)
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
	}
}
