package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/leaf"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/life1"
	syncthingconfig "github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
)

type fakeLifecycle struct{ closed bool }

func (lifecycle *fakeLifecycle) Close() error {
	lifecycle.closed = true
	return nil
}

func TestBootstrapOrdersLockDirectoriesAndLifecycle(t *testing.T) {
	config := testConfig(t)
	lifecycle := &fakeLifecycle{}
	connectCalls := 0
	runner := Runner{
		Config: config,
		Connect: func(_ context.Context, got life1.Config) (Lifecycle, life1.GameState, error) {
			connectCalls++
			if got.SocketPath != config.DaemonSocket || got.ServiceID != ServiceID || got.Mode != life1.ModeNotify {
				t.Fatalf("LIFE-1 config = %+v", got)
			}
			secondLock, err := acquireControllerLock(config.RuntimeDir)
			if secondLock != nil {
				_ = secondLock.Close()
			}
			if !errors.Is(err, ErrAlreadyRunning) {
				t.Fatalf("singleton was not held before LIFE-1 connect: %v", err)
			}
			for _, name := range []string{"config", "data", "leaf", "backups"} {
				path := filepath.Join(config.UserdataPath, leaf.AppStateName, name)
				if info, err := os.Stat(path); err != nil || !info.IsDir() {
					t.Fatalf("durable directory missing before LIFE-1 connect: %s", path)
				}
			}
			return lifecycle, life1.GameState{Active: false}, nil
		},
	}

	session, err := runner.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if connectCalls != 1 {
		t.Fatalf("connect calls = %d, want 1", connectCalls)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if !lifecycle.closed {
		t.Fatal("session did not close LIFE-1 connection")
	}

	lock, err := acquireControllerLock(config.RuntimeDir)
	if err != nil {
		t.Fatalf("singleton remained locked after close: %v", err)
	}
	_ = lock.Close()
}

func TestBootstrapRetriesUnavailableJawakaWithoutDroppingLock(t *testing.T) {
	config := testConfig(t)
	config.RetryDelay = time.Millisecond
	calls := 0
	runner := Runner{
		Config: config,
		Connect: func(_ context.Context, _ life1.Config) (Lifecycle, life1.GameState, error) {
			calls++
			if calls == 1 {
				if lock, err := acquireControllerLock(config.RuntimeDir); lock != nil || !errors.Is(err, ErrAlreadyRunning) {
					t.Fatalf("singleton dropped while Jawaka unavailable: lock=%v err=%v", lock, err)
				}
				return nil, life1.GameState{}, life1.ErrUnavailable
			}
			return &fakeLifecycle{}, life1.GameState{}, nil
		},
	}
	session, err := runner.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if calls != 2 {
		t.Fatalf("connect calls = %d, want 2", calls)
	}
}

func TestBootstrapModeStopExitsBeforeContinuation(t *testing.T) {
	config := testConfig(t)
	config.Mode = life1.ModeStop
	lifecycle := &fakeLifecycle{}
	runner := Runner{
		Config: config,
		Connect: func(_ context.Context, _ life1.Config) (Lifecycle, life1.GameState, error) {
			return lifecycle, life1.GameState{Active: true, LaunchID: "launch", SourceID: "primary"}, nil
		},
		Recover: func(string, syncthingconfig.SyncFilesystemFunc) (syncthingconfig.RecoveryResult, error) {
			t.Fatal("config recovery ran during intentional mode-stop exit")
			return syncthingconfig.RecoveryResult{}, nil
		},
	}
	if _, err := runner.Bootstrap(context.Background()); !errors.Is(err, ErrLifecycleStop) {
		t.Fatalf("Bootstrap() error = %v, want %v", err, ErrLifecycleStop)
	}
	if !lifecycle.closed {
		t.Fatal("mode-stop path did not close LIFE-1 connection")
	}
}

func TestBootstrapQueriesLifecycleBeforeConfigRecovery(t *testing.T) {
	config := testConfig(t)
	connected := false
	runner := Runner{
		Config: config,
		Connect: func(_ context.Context, _ life1.Config) (Lifecycle, life1.GameState, error) {
			connected = true
			return &fakeLifecycle{}, life1.GameState{}, nil
		},
		Recover: func(path string, _ syncthingconfig.SyncFilesystemFunc) (syncthingconfig.RecoveryResult, error) {
			if !connected {
				t.Fatal("config recovery ran before LIFE-1 reconciliation")
			}
			if path != config.ConfigDir {
				t.Fatalf("recovery path = %s, want %s", path, config.ConfigDir)
			}
			return syncthingconfig.RecoveryResult{State: syncthingconfig.RecoveryClean}, nil
		},
	}
	session, err := runner.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if session.Recovery.State != syncthingconfig.RecoveryClean {
		t.Fatalf("recovery = %+v", session.Recovery)
	}
}

func TestBootstrapRejectsSymlinkedDurableRoot(t *testing.T) {
	config := testConfig(t)
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(config.UserdataPath, leaf.AppStateName)); err != nil {
		t.Fatal(err)
	}
	runner := Runner{
		Config: config,
		Connect: func(_ context.Context, _ life1.Config) (Lifecycle, life1.GameState, error) {
			t.Fatal("LIFE-1 connect ran after unsafe durable path")
			return nil, life1.GameState{}, nil
		},
	}
	if _, err := runner.Bootstrap(context.Background()); err == nil {
		t.Fatal("Bootstrap accepted symlinked durable root")
	}
}

func testConfig(t *testing.T) Config {
	t.Helper()
	base := t.TempDir()
	runtimeDir := filepath.Join(base, "runtime", "services", ServiceDirName)
	userdata := filepath.Join(base, "userdata")
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(userdata, 0o700); err != nil {
		t.Fatal(err)
	}
	return Config{
		RuntimeDir: runtimeDir, UserdataPath: userdata,
		ConfigDir:    filepath.Join(userdata, leaf.AppStateName, "config"),
		DaemonSocket: filepath.Join(base, "jawakad.sock"),
		Mode:         life1.ModeNotify, AckMS: life1.DefaultAckMS,
	}
}
