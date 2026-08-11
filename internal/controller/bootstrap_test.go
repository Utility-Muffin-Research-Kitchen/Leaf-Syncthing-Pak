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

type fakeLifecycle struct {
	closed  bool
	next    func(context.Context) (life1.Event, error)
	ready   func(string) error
	waiting func(string, int, int64) error
	stop    func(string) error
	reject  func(string, string) error
}

func (lifecycle *fakeLifecycle) Close() error {
	lifecycle.closed = true
	return nil
}

func (lifecycle *fakeLifecycle) Next(ctx context.Context) (life1.Event, error) {
	if lifecycle.next != nil {
		return lifecycle.next(ctx)
	}
	<-ctx.Done()
	return life1.Event{}, ctx.Err()
}

func (lifecycle *fakeLifecycle) SendReady(launchID string) error {
	if lifecycle.ready != nil {
		return lifecycle.ready(launchID)
	}
	return nil
}

func (lifecycle *fakeLifecycle) SendWaiting(launchID string, pendingItems int, pendingBytes int64) error {
	if lifecycle.waiting != nil {
		return lifecycle.waiting(launchID, pendingItems, pendingBytes)
	}
	return nil
}

func (lifecycle *fakeLifecycle) SendStop(launchID string) error {
	if lifecycle.stop != nil {
		return lifecycle.stop(launchID)
	}
	return nil
}

func (lifecycle *fakeLifecycle) SendError(launchID, reason string) error {
	if lifecycle.reject != nil {
		return lifecycle.reject(launchID, reason)
	}
	return nil
}

func TestBootstrapOrdersLockDirectoriesAndLifecycle(t *testing.T) {
	config := testConfig(t)
	lifecycle := &fakeLifecycle{}
	connectCalls := 0
	runner := Runner{
		Config:         config,
		EnsureIdentity: successfulIdentity,
		ApplyPause:     successfulPause,
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
			for _, name := range []string{"data", "leaf", "backups"} {
				path := filepath.Join(config.UserdataPath, leaf.AppStateName, name)
				if info, err := os.Stat(path); err != nil || !info.IsDir() {
					t.Fatalf("durable directory missing before LIFE-1 connect: %s", path)
				}
			}
			if _, err := os.Lstat(config.ConfigDir); !os.IsNotExist(err) {
				t.Fatalf("factory-clean config directory was created before generation: %v", err)
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
		Config:         config,
		EnsureIdentity: successfulIdentity,
		ApplyPause:     successfulPause,
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
		EnsureIdentity: func(context.Context, syncthingconfig.IdentityOptions, syncthingconfig.RecoveryResult) (syncthingconfig.Identity, error) {
			t.Fatal("identity generation ran during intentional mode-stop exit")
			return syncthingconfig.Identity{}, nil
		},
		ApplyPause: func(string, map[string]bool, syncthingconfig.SyncFilesystemFunc) (syncthingconfig.PauseEditResult, error) {
			t.Fatal("offline pause edit ran during intentional mode-stop exit")
			return syncthingconfig.PauseEditResult{}, nil
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
		EnsureIdentity: successfulIdentity,
		ApplyPause:     successfulPause,
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

func TestBootstrapForcesManagedFoldersPausedBeforeSpawn(t *testing.T) {
	config := testConfig(t)
	card := cards.Card{Identity: cards.Identity{Version: 1, ID: "00112233445566778899aabbccddeeff"}}
	folderID, markerName, err := cards.BindingNames(card.Identity.ID, "saves")
	if err != nil {
		t.Fatal(err)
	}
	folder := syncthingconfig.ConfiguredFolder{ID: folderID, Kind: "saves", MarkerName: markerName}
	runner := Runner{
		Config: config,
		Connect: func(context.Context, life1.Config) (Lifecycle, life1.GameState, error) {
			return &fakeLifecycle{}, life1.GameState{}, nil
		},
		EnsureIdentity: successfulIdentity,
		LoadFolders: func(string) ([]syncthingconfig.ConfiguredFolder, error) {
			return []syncthingconfig.ConfiguredFolder{folder}, nil
		},
		LoadCards: func(leaf.SourceList, string) ([]cards.Card, error) { return []cards.Card{card}, nil },
		ApplyPause: func(_ string, desired map[string]bool, _ syncthingconfig.SyncFilesystemFunc) (syncthingconfig.PauseEditResult, error) {
			if len(desired) != 1 || !desired[folderID] {
				t.Fatalf("offline pause set = %#v", desired)
			}
			return syncthingconfig.PauseEditResult{Changed: true}, nil
		},
	}
	session, err := runner.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if len(session.Folders) != 1 || !session.Folders[0].Paused || !session.PauseEdit.Changed {
		t.Fatalf("managed folder session = %+v", session)
	}
}

func TestBootstrapLoadsExternalFolderIDFromDurableBinding(t *testing.T) {
	config := testConfig(t)
	cardID := "00112233445566778899aabbccddeeff"
	_, markerName, err := cards.BindingNames(cardID, "saves")
	if err != nil {
		t.Fatal(err)
	}
	saves := filepath.Join(config.Sources[0].Root, "Saves")
	if err := os.MkdirAll(filepath.Join(saves, markerName), 0o700); err != nil {
		t.Fatal(err)
	}
	config.Sources[0].SavesPath = saves
	config.Sources[0].StatesPath = filepath.Join(config.Sources[0].Root, "States")
	card := cards.Card{
		Source: config.Sources[0], Identity: cards.Identity{Version: 1, ID: cardID},
		State: cards.StateEnrolled, Present: true, Writable: true,
	}
	folder := syncthingconfig.ConfiguredFolder{
		ID: "retro-saves", Label: "Retro Saves", Kind: "saves", Path: saves,
		Type: "sendonly", MarkerName: markerName, Paused: true,
	}
	controlPath := filepath.Join(config.UserdataPath, leaf.AppStateName, "leaf", folderControlStateName)
	controls, err := newFolderControlStore(controlPath, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := controls.BeginAdd(folder, card); err != nil {
		t.Fatal(err)
	}
	runner := Runner{
		Config: config,
		Connect: func(context.Context, life1.Config) (Lifecycle, life1.GameState, error) {
			return &fakeLifecycle{}, life1.GameState{}, nil
		},
		EnsureIdentity: func(ctx context.Context, options syncthingconfig.IdentityOptions, recovery syncthingconfig.RecoveryResult) (syncthingconfig.Identity, error) {
			identity, err := successfulIdentity(ctx, options, recovery)
			if err != nil {
				return syncthingconfig.Identity{}, err
			}
			contents := `<configuration version="52">` +
				`<folder id="retro-saves" label="Retro Saves" path="` + saves + `" type="sendonly">` +
				`<paused>false</paused><markerName>` + markerName + `</markerName></folder>` +
				`<folder id="leaf-saves-not-a-binding" path="/unmanaged"></folder>` +
				`</configuration>`
			if err := os.WriteFile(filepath.Join(options.ConfigDir, "config.xml"), []byte(contents), 0o600); err != nil {
				return syncthingconfig.Identity{}, err
			}
			return identity, nil
		},
		LoadCards: func(leaf.SourceList, string) ([]cards.Card, error) {
			return []cards.Card{card}, nil
		},
		ApplyPause: func(_ string, desired map[string]bool, _ syncthingconfig.SyncFilesystemFunc) (syncthingconfig.PauseEditResult, error) {
			if len(desired) != 1 || !desired[folder.ID] {
				t.Fatalf("external offline pause set = %#v", desired)
			}
			return syncthingconfig.PauseEditResult{Changed: true}, nil
		},
	}
	session, err := runner.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if len(session.Folders) != 1 || session.Folders[0].ID != folder.ID || session.Folders[0].Kind != "saves" || !session.Folders[0].Paused {
		t.Fatalf("external managed folder session = %+v", session.Folders)
	}
	if session.FolderControls.Snapshot()[folder.ID].PendingAdd {
		t.Fatalf("present pending add was not activated: %+v", session.FolderControls.Snapshot())
	}
}

func TestBootstrapRollsBackPendingAddMissingFromUpstream(t *testing.T) {
	config := testConfig(t)
	card := cards.Card{Identity: cards.Identity{Version: 1, ID: "00112233445566778899aabbccddeeff"}}
	_, markerName, err := cards.BindingNames(card.Identity.ID, "saves")
	if err != nil {
		t.Fatal(err)
	}
	folder := syncthingconfig.ConfiguredFolder{ID: "retro-saves", Kind: "saves", MarkerName: markerName}
	controlPath := filepath.Join(config.UserdataPath, leaf.AppStateName, "leaf", folderControlStateName)
	controls, err := newFolderControlStore(controlPath, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := controls.BeginAdd(folder, card); err != nil {
		t.Fatal(err)
	}
	runner := Runner{
		Config: config,
		Connect: func(context.Context, life1.Config) (Lifecycle, life1.GameState, error) {
			return &fakeLifecycle{}, life1.GameState{}, nil
		},
		EnsureIdentity: successfulIdentity,
		LoadCards: func(leaf.SourceList, string) ([]cards.Card, error) {
			return []cards.Card{card}, nil
		},
		ApplyPause: successfulPause,
	}
	session, err := runner.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if len(session.Folders) != 0 || len(session.FolderControls.Snapshot()) != 0 {
		t.Fatalf("missing pending add survived recovery: folders=%+v controls=%+v", session.Folders, session.FolderControls.Snapshot())
	}
}

func TestBootstrapOfflinePauseFollowsDurableFirstSyncAndCardSafety(t *testing.T) {
	for _, test := range []struct {
		name           string
		seedCompletion bool
		wantPaused     bool
	}{
		{name: "durably completed safe folder", seedCompletion: true, wantPaused: false},
		{name: "missing completion marker", seedCompletion: false, wantPaused: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := testConfig(t)
			cardID := "00112233445566778899aabbccddeeff"
			folderID, marker, err := cards.BindingNames(cardID, "saves")
			if err != nil {
				t.Fatal(err)
			}
			saves := filepath.Join(config.Sources[0].Root, "Saves")
			if err := os.MkdirAll(filepath.Join(saves, marker), 0o700); err != nil {
				t.Fatal(err)
			}
			config.Sources[0].SavesPath = saves
			config.Sources[0].StatesPath = filepath.Join(config.Sources[0].Root, "States")
			card := cards.Card{
				Source: config.Sources[0], Identity: cards.Identity{Version: 1, ID: cardID},
				State: cards.StateEnrolled, Present: true, Writable: true,
			}
			folder := syncthingconfig.ConfiguredFolder{
				ID: folderID, Label: "Leaf Saves", Kind: "saves", Path: saves,
				Type: "sendonly", MarkerName: marker, Paused: true,
			}
			controlPath := filepath.Join(config.UserdataPath, leaf.AppStateName, "leaf", folderControlStateName)
			controls, err := newFolderControlStore(controlPath, []syncthingconfig.ConfiguredFolder{folder}, []cards.Card{card})
			if err != nil {
				t.Fatal(err)
			}
			if test.seedCompletion {
				manager, err := newFirstSyncManager([]syncthingconfig.ConfiguredFolder{folder}, []cards.Card{card}, controls, fixedFirstSyncOptions(nil, ""))
				if err != nil {
					t.Fatal(err)
				}
				if err := manager.Complete(folder, card, controls, true); err != nil {
					t.Fatal(err)
				}
			} else if err := controls.SetFirstSync(folder.ID, false); err != nil {
				t.Fatal(err)
			}

			runner := Runner{
				Config: config,
				Connect: func(context.Context, life1.Config) (Lifecycle, life1.GameState, error) {
					return &fakeLifecycle{}, life1.GameState{}, nil
				},
				EnsureIdentity: successfulIdentity,
				LoadFolders: func(string) ([]syncthingconfig.ConfiguredFolder, error) {
					return []syncthingconfig.ConfiguredFolder{folder}, nil
				},
				LoadCards: func(leaf.SourceList, string) ([]cards.Card, error) {
					return []cards.Card{card}, nil
				},
				FirstSyncOptions: fixedFirstSyncOptions(nil, ""),
				ApplyPause: func(_ string, desired map[string]bool, _ syncthingconfig.SyncFilesystemFunc) (syncthingconfig.PauseEditResult, error) {
					if desired[folder.ID] != test.wantPaused {
						t.Fatalf("offline pause = %v, want %v", desired[folder.ID], test.wantPaused)
					}
					return syncthingconfig.PauseEditResult{}, nil
				},
			}
			session, err := runner.Bootstrap(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			if session.Folders[0].Paused != test.wantPaused || session.FolderControls.Snapshot()[folder.ID].FirstSync != test.wantPaused {
				t.Fatalf("session folder = %+v, controls=%+v", session.Folders[0], session.FolderControls.Snapshot())
			}
		})
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
	base, err := os.MkdirTemp("/tmp", "leaf-syncthing-controller-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	runtimeDir := filepath.Join(base, "runtime", "services", ServiceDirName)
	userdata := filepath.Join(base, "userdata")
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(userdata, 0o700); err != nil {
		t.Fatal(err)
	}
	return Config{
		RuntimeDir: runtimeDir, UserdataPath: userdata, LogsPath: filepath.Join(userdata, "logs"),
		ConfigDir:       filepath.Join(userdata, leaf.AppStateName, "config"),
		DataDir:         filepath.Join(userdata, leaf.AppStateName, "data"),
		UpstreamBinary:  filepath.Join(base, "syncthing"),
		UpstreamVersion: PinnedUpstreamVersion,
		GUISocket:       filepath.Join(base, "runtime", "services", ServiceDirName, "syncthing-gui.sock"),
		ControlSocket:   filepath.Join(base, "runtime", "services", ServiceDirName, "control.sock"),
		DaemonSocket:    filepath.Join(base, "jawakad.sock"),
		Sources: leaf.SourceList{{
			ID: "primary", Root: base, Primary: true, UserdataPath: userdata,
		}},
		Mode: life1.ModeNotify, AckMS: life1.DefaultAckMS,
	}
}

func successfulIdentity(_ context.Context, options syncthingconfig.IdentityOptions, recovery syncthingconfig.RecoveryResult) (syncthingconfig.Identity, error) {
	if err := os.MkdirAll(options.ConfigDir, 0o700); err != nil {
		return syncthingconfig.Identity{}, err
	}
	if err := os.WriteFile(filepath.Join(options.ConfigDir, "config.xml"), []byte(`<configuration version="52"></configuration>`), 0o600); err != nil {
		return syncthingconfig.Identity{}, err
	}
	return syncthingconfig.Identity{
		DeviceID: "fixture-device", UpstreamVersion: options.UpstreamVersion, ConfigVersion: 52,
	}, nil
}

func successfulPause(string, map[string]bool, syncthingconfig.SyncFilesystemFunc) (syncthingconfig.PauseEditResult, error) {
	return syncthingconfig.PauseEditResult{}, nil
}
