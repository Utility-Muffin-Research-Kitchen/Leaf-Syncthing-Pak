package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/life1"
	syncthingconfig "github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/uicontrol"
)

const StopGrace = 10 * time.Second

type UpstreamProcess interface {
	Done() <-chan error
	Shutdown(context.Context) error
}

type StartProcessFunc func(context.Context, syncthingconfig.ProcessOptions) (UpstreamProcess, error)

type lifecycleResult struct {
	event life1.Event
	err   error
}

// Run owns the foreground service lifetime. It never restarts upstream inside
// the same supervised generation; any unexpected upstream or LIFE-1 failure is
// returned to Jawaka for reserved-group cleanup and restart policy.
func (runner Runner) Run(ctx context.Context) error {
	session, err := runner.Bootstrap(ctx)
	if err != nil {
		if errors.Is(err, ErrLifecycleStop) {
			return nil
		}
		return err
	}
	defer session.Close()

	startProcess := runner.StartProcess
	if startProcess == nil {
		startProcess = func(ctx context.Context, options syncthingconfig.ProcessOptions) (UpstreamProcess, error) {
			return syncthingconfig.StartProcess(ctx, options)
		}
	}
	upstream, err := startProcess(ctx, syncthingconfig.ProcessOptions{
		Binary: runner.Config.UpstreamBinary, ConfigDir: runner.Config.ConfigDir,
		DataDir: runner.Config.DataDir, GUISocket: runner.Config.GUISocket,
		Stdout: os.Stdout, Stderr: os.Stderr,
	})
	if err != nil {
		return fmt.Errorf("start upstream: %w", err)
	}
	gameState := session.State
	var status atomic.Value
	status.Store(controlStatus(session, gameState))
	control, err := uicontrol.Listen(runner.Config.ControlSocket, func() uicontrol.Status {
		return status.Load().(uicontrol.Status)
	})
	if err != nil {
		_ = shutdownUpstream(upstream)
		return fmt.Errorf("start UI control socket: %w", err)
	}
	defer control.Close()

	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	lifecycleEvents := make(chan lifecycleResult, 1)
	go func() {
		for {
			event, err := session.Lifecycle.Next(runContext)
			select {
			case lifecycleEvents <- lifecycleResult{event: event, err: err}:
			case <-runContext.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return shutdownUpstream(upstream)
		case err := <-upstream.Done():
			if ctx.Err() != nil {
				return shutdownUpstream(upstream)
			}
			return fmt.Errorf("upstream exited unexpectedly: %v", err)
		case err := <-control.Done():
			if err == nil {
				err = errors.New("control socket stopped")
			}
			if shutdownErr := shutdownUpstream(upstream); shutdownErr != nil {
				return fmt.Errorf("UI control socket failed (%v); %w", err, shutdownErr)
			}
			return fmt.Errorf("UI control socket failed: %w", err)
		case result := <-lifecycleEvents:
			if result.err != nil {
				if ctx.Err() != nil {
					return shutdownUpstream(upstream)
				}
				return fmt.Errorf("LIFE-1 subscription failed: %w", result.err)
			}
			if err := handleLifecycleEvent(session.Lifecycle, result.event); err != nil {
				return err
			}
			gameState = reconcileGameState(gameState, result.event)
			status.Store(controlStatus(session, gameState))
		}
	}
}

func controlStatus(session *Session, game life1.GameState) uicontrol.Status {
	return uicontrol.Status{
		Controller: "running",
		Upstream: uicontrol.UpstreamStatus{
			State: "running", Version: session.Identity.UpstreamVersion, DeviceID: session.Identity.DeviceID,
		},
		Game:         uicontrol.GameStatus{Active: game.Active, LaunchID: game.LaunchID, SourceID: game.SourceID},
		Recovery:     uicontrol.RecoveryStatus{State: "ready", Changed: session.Recovery.Changed},
		Capabilities: []string{uicontrol.OperationGet},
	}
}

func reconcileGameState(current life1.GameState, event life1.Event) life1.GameState {
	switch event.Name {
	case "game.start":
		return life1.GameState{
			Active: true, LaunchID: event.LaunchID, SourceID: event.SourceID,
			SavesPath: event.SavesPath, StatesPath: event.StatesPath,
		}
	case "game.cancel", "game.finish":
		if current.LaunchID == event.LaunchID {
			return life1.GameState{}
		}
	}
	return current
}

func shutdownUpstream(upstream UpstreamProcess) error {
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), StopGrace)
	defer shutdownCancel()
	if err := upstream.Shutdown(shutdownContext); err != nil {
		return fmt.Errorf("stop upstream: %w", err)
	}
	return nil
}

func handleLifecycleEvent(lifecycle Lifecycle, event life1.Event) error {
	switch event.Name {
	case "game.start":
		// B1 has no enrolled folders yet. With no possible binding on the active
		// card there is no writer to pause, so LIFE-1 requires immediate ready.
		if err := lifecycle.SendReady(event.LaunchID); err != nil {
			return fmt.Errorf("answer game.start: %w", err)
		}
	case "game.cancel", "game.finish":
		// No B1 folder pause reason exists to release.
	default:
		return fmt.Errorf("unsupported LIFE-1 event %q", event.Name)
	}
	return nil
}
