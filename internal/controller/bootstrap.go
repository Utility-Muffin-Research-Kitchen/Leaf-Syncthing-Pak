// Package controller owns the resident Leaf Syncthing service process.
package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/cards"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/leaf"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/life1"
	syncthingconfig "github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
	"golang.org/x/sys/unix"
)

const (
	ServiceID             = "org.umrk.syncthing"
	ServiceDirName        = "org.umrk.syncthing"
	ControllerLock        = "controller.lock"
	DefaultRetry          = time.Second
	ServiceLeaseFD        = 3
	PinnedUpstreamVersion = "v2.1.2"
)

var (
	ErrAlreadyRunning = errors.New("leaf-syncthing: controller is already running")
	ErrLifecycleStop  = errors.New("leaf-syncthing: active game requires an intentional policy stop")
)

type Config struct {
	RuntimeDir      string
	UserdataPath    string
	LogsPath        string
	ConfigDir       string
	DataDir         string
	UpstreamBinary  string
	UpstreamVersion string
	GUISocket       string
	ControlSocket   string
	DaemonSocket    string
	Sources         leaf.SourceList
	Mode            life1.Mode
	AckMS           int
	WaitMS          int
	RetryDelay      time.Duration
}

type Lifecycle interface {
	Close() error
	Next(context.Context) (life1.Event, error)
	SendReady(string) error
	SendError(string, string) error
}

type ConnectFunc func(context.Context, life1.Config) (Lifecycle, life1.GameState, error)
type RecoverConfigFunc func(string, syncthingconfig.SyncFilesystemFunc) (syncthingconfig.RecoveryResult, error)
type EnsureIdentityFunc func(context.Context, syncthingconfig.IdentityOptions, syncthingconfig.RecoveryResult) (syncthingconfig.Identity, error)
type ApplyPauseFunc func(string, map[string]bool, syncthingconfig.SyncFilesystemFunc) (syncthingconfig.PauseEditResult, error)
type LoadManagedFoldersFunc func(string) ([]syncthingconfig.ConfiguredFolder, error)

type Runner struct {
	Config            Config
	Connect           ConnectFunc
	Recover           RecoverConfigFunc
	EnsureIdentity    EnsureIdentityFunc
	ApplyPause        ApplyPauseFunc
	LoadFolders       LoadManagedFoldersFunc
	StartProcess      StartProcessFunc
	LoadCards         LoadCardsFunc
	EnrollCard        EnrollCardFunc
	FirstSyncOptions  firstSyncOptions
	OnboardingOptions onboardingOptions
	Logf              func(string, ...any)
}

type Session struct {
	State          life1.GameState
	Recovery       syncthingconfig.RecoveryResult
	Identity       syncthingconfig.Identity
	PauseEdit      syncthingconfig.PauseEditResult
	Folders        []syncthingconfig.ConfiguredFolder
	Inventory      []cards.Card
	FolderControls *folderControlStore
	FirstSync      *firstSyncManager
	Lifecycle      Lifecycle
	lock           *os.File
}

func LoadConfig() (Config, error) {
	environment, err := leaf.LoadEnvironment()
	if err != nil {
		return Config{}, err
	}
	if !environment.SourcePathsV2 {
		return Config{}, errors.New("leaf-syncthing: source-paths-v2 with aligned USERDATA_PATHS is required")
	}
	socket, err := life1.ResolveSocket(os.Getenv, environment.RuntimePath)
	if err != nil {
		return Config{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return Config{}, fmt.Errorf("locate controller executable: %w", err)
	}
	return Config{
		RuntimeDir:      filepath.Join(environment.RuntimePath, "services", ServiceDirName),
		UserdataPath:    environment.UserdataPath,
		LogsPath:        environment.LogsPath,
		ConfigDir:       filepath.Join(environment.StateDir(), "config"),
		DataDir:         filepath.Join(environment.StateDir(), "data"),
		UpstreamBinary:  filepath.Join(filepath.Dir(executable), "syncthing"),
		UpstreamVersion: PinnedUpstreamVersion,
		GUISocket:       filepath.Join(environment.RuntimePath, "services", ServiceDirName, "syncthing-gui.sock"),
		ControlSocket:   filepath.Join(environment.RuntimePath, "services", ServiceDirName, "control.sock"),
		DaemonSocket:    socket,
		Sources:         environment.Sources,
		Mode:            life1.ModeStop,
		AckMS:           life1.DefaultAckMS,
		WaitMS:          life1.DefaultWaitMS,
		RetryDelay:      DefaultRetry,
	}, nil
}

// Bootstrap implements the first six normative SYNC-1 startup steps. It
// returns with the singleton lock and LIFE-1 connection held so no caller can
// accidentally spawn upstream outside their protection.
func (runner Runner) Bootstrap(ctx context.Context) (*Session, error) {
	if err := runner.Config.validate(); err != nil {
		return nil, err
	}

	lock, err := acquireControllerLock(runner.Config.RuntimeDir)
	if err != nil {
		return nil, err
	}
	closeLock := true
	defer func() {
		if closeLock {
			_ = lock.Close()
		}
	}()
	if _, err := RecoverReset(runner.Config, ResetOptions{}); err != nil {
		return nil, fmt.Errorf("recover destructive reset: %w", err)
	}

	if err := prepareDurableDirectories(runner.Config.UserdataPath); err != nil {
		return nil, err
	}

	lifecycle, state, err := runner.establishLifecycle(ctx)
	if err != nil {
		return nil, err
	}

	if runner.Config.Mode == life1.ModeStop && state.Active {
		_ = lifecycle.Close()
		return nil, ErrLifecycleStop
	}

	recoverConfig := runner.Recover
	if recoverConfig == nil {
		recoverConfig = syncthingconfig.RecoverConfig
	}
	recovery, err := recoverConfig(runner.Config.ConfigDir, nil)
	if err != nil {
		_ = lifecycle.Close()
		return nil, fmt.Errorf("recover upstream config: %w", err)
	}
	ensureIdentity := runner.EnsureIdentity
	if ensureIdentity == nil {
		ensureIdentity = syncthingconfig.EnsureIdentity
	}
	identity, err := ensureIdentity(ctx, syncthingconfig.IdentityOptions{
		Binary: runner.Config.UpstreamBinary, ConfigDir: runner.Config.ConfigDir,
		DataDir: runner.Config.DataDir, UpstreamVersion: runner.Config.UpstreamVersion,
		GUISocket: runner.Config.GUISocket,
	}, recovery)
	if err != nil {
		_ = lifecycle.Close()
		return nil, fmt.Errorf("ensure upstream identity: %w", err)
	}
	controlPath := filepath.Join(runner.Config.UserdataPath, leaf.AppStateName, "leaf", folderControlStateName)
	storedControls, _, _, controlSchema, _, err := readFolderControlState(controlPath)
	if err != nil {
		_ = lifecycle.Close()
		return nil, fmt.Errorf("load folder control state: %w", err)
	}
	legacyFolders := []syncthingconfig.ConfiguredFolder{}
	if runner.LoadFolders != nil || controlSchema != 2 {
		loadFolders := runner.LoadFolders
		if loadFolders == nil {
			loadFolders = syncthingconfig.ReadManagedFolders
		}
		legacyFolders, err = loadFolders(runner.Config.ConfigDir)
		if err != nil {
			_ = lifecycle.Close()
			return nil, fmt.Errorf("read managed folders: %w", err)
		}
	}
	inventory := []cards.Card{}
	if len(legacyFolders) > 0 || len(storedControls) > 0 {
		loadCards := runner.LoadCards
		if loadCards == nil {
			loadCards = func(sources leaf.SourceList, registryDirectory string) ([]cards.Card, error) {
				live, err := cards.InspectSources(sources, cards.Options{})
				if err != nil {
					return nil, err
				}
				return cards.ReconcileRegistry(registryDirectory, sources, live, nil)
			}
		}
		registryDirectory := filepath.Join(runner.Config.UserdataPath, leaf.AppStateName, "leaf")
		inventory, err = loadCards(runner.Config.Sources, registryDirectory)
		if err != nil {
			_ = lifecycle.Close()
			return nil, fmt.Errorf("verify cards before offline pause: %w", err)
		}
	}
	folderControls, err := newFolderControlStore(controlPath, legacyFolders, inventory)
	if err != nil {
		_ = lifecycle.Close()
		return nil, fmt.Errorf("load folder control state: %w", err)
	}
	folders := legacyFolders
	if runner.LoadFolders == nil {
		folders, err = syncthingconfig.ReadAvailableManagedFoldersForBindings(runner.Config.ConfigDir, folderControls.BindingKinds())
		if err != nil {
			_ = lifecycle.Close()
			return nil, fmt.Errorf("read bound managed folders: %w", err)
		}
		configured := make(map[string]bool, len(folders))
		for _, folder := range folders {
			configured[folder.ID] = true
		}
		for folderID, record := range folderControls.Snapshot() {
			switch {
			case configured[folderID] && record.PendingAdd:
				err = folderControls.Activate(folderID)
			case !configured[folderID] && record.PendingAdd:
				err = folderControls.Remove(folderID)
			case !configured[folderID] && record.PendingStop:
				// Runtime recovery removes the local marker and binding after
				// verifying the upstream folder is still absent.
			case !configured[folderID]:
				err = errors.New("active registered folder is missing from upstream config")
			}
			if err != nil {
				_ = lifecycle.Close()
				return nil, fmt.Errorf("reconcile folder add transaction: %w", err)
			}
		}
	}
	firstSync, err := newFirstSyncManager(folders, inventory, folderControls, runner.FirstSyncOptions)
	if err != nil {
		_ = lifecycle.Close()
		return nil, fmt.Errorf("recover first-sync state: %w", err)
	}
	pauseSet := requiredOfflinePauseSet(folders, inventory, folderControls.Snapshot())
	applyPause := runner.ApplyPause
	if applyPause == nil {
		applyPause = syncthingconfig.ApplyOfflinePauseSet
	}
	pauseEdit, err := applyPause(runner.Config.ConfigDir, pauseSet, nil)
	if err != nil {
		_ = lifecycle.Close()
		return nil, fmt.Errorf("apply offline pause set: %w", err)
	}
	for index := range folders {
		folders[index].Paused = pauseSet[folders[index].ID]
	}

	closeLock = false
	return &Session{
		State: state, Recovery: recovery, Identity: identity, PauseEdit: pauseEdit, Folders: folders,
		Inventory: inventory, FolderControls: folderControls, FirstSync: firstSync,
		Lifecycle: lifecycle, lock: lock,
	}, nil
}

func (runner Runner) establishLifecycle(ctx context.Context) (Lifecycle, life1.GameState, error) {
	connect := runner.Connect
	if connect == nil {
		connect = func(ctx context.Context, config life1.Config) (Lifecycle, life1.GameState, error) {
			return life1.Connect(ctx, config)
		}
	}
	retryDelay := runner.Config.RetryDelay
	if retryDelay == 0 {
		retryDelay = DefaultRetry
	}
	for {
		lifecycle, state, err := connect(ctx, life1.Config{
			SocketPath: runner.Config.DaemonSocket,
			ServiceID:  ServiceID,
			Mode:       runner.Config.Mode,
			AckMS:      runner.Config.AckMS,
			WaitMS:     runner.Config.WaitMS,
		})
		if err == nil {
			return lifecycle, state, nil
		}
		if !errors.Is(err, life1.ErrUnavailable) {
			return nil, life1.GameState{}, fmt.Errorf("establish LIFE-1 subscription: %w", err)
		}
		if runner.Logf != nil {
			runner.Logf("Jawaka unavailable; upstream remains stopped: %v", err)
		}
		timer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, life1.GameState{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (session *Session) Close() error {
	if session == nil {
		return nil
	}
	var result error
	if session.Lifecycle != nil {
		result = session.Lifecycle.Close()
	}
	if session.lock != nil {
		if err := session.lock.Close(); result == nil {
			result = err
		}
	}
	return result
}

func (config Config) validate() error {
	if config.RuntimeDir == "" || config.UserdataPath == "" || config.LogsPath == "" || config.ConfigDir == "" || config.DataDir == "" ||
		config.UpstreamBinary == "" || config.UpstreamVersion == "" || config.GUISocket == "" ||
		config.ControlSocket == "" || config.DaemonSocket == "" || len(config.Sources) == 0 {
		return errors.New("leaf-syncthing: runtime, userdata, config, data, upstream, and daemon values are required")
	}
	for _, source := range config.Sources {
		if source.ID == "" || source.Root == "" || source.UserdataPath == "" {
			return errors.New("leaf-syncthing: every PATH-2 source requires id, root, and userdata")
		}
		if _, err := leaf.RelativeWithin(source.Root, source.UserdataPath); err != nil {
			return fmt.Errorf("leaf-syncthing: source %s userdata is outside its card: %w", source.ID, err)
		}
	}
	if config.Mode != life1.ModeNotify && config.Mode != life1.ModeStop {
		return fmt.Errorf("leaf-syncthing: unsupported game mode %q", config.Mode)
	}
	if config.AckMS < 0 || config.WaitMS < 0 || config.RetryDelay < 0 {
		return errors.New("leaf-syncthing: lifecycle timings must be non-negative")
	}
	return nil
}

func acquireControllerLock(runtimeDir string) (*os.File, error) {
	info, err := os.Lstat(runtimeDir)
	if err != nil {
		return nil, fmt.Errorf("validate service runtime directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("validate service runtime directory: not a real directory")
	}

	path := filepath.Join(runtimeDir, ControllerLock)
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open controller lock: %w", err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("lock controller singleton: %w", err)
	}
	return lock, nil
}

func prepareDurableDirectories(userdataPath string) error {
	info, err := os.Lstat(userdataPath)
	if err != nil {
		return fmt.Errorf("validate USERDATA_PATH before creating state: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("validate USERDATA_PATH before creating state: not a real directory")
	}

	stateRoot := filepath.Join(userdataPath, leaf.AppStateName)
	if err := ensureOwnedDirectory(stateRoot, 0o700); err != nil {
		return err
	}
	for _, name := range []string{"data", "leaf", "backups"} {
		if err := ensureOwnedDirectory(filepath.Join(stateRoot, name), 0o700); err != nil {
			return err
		}
	}
	configDir := filepath.Join(stateRoot, "config")
	if info, err := os.Lstat(configDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("validate owned directory %s: not a real directory", configDir)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("validate owned directory %s: %w", configDir, err)
	}
	return nil
}

func ensureOwnedDirectory(path string, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("validate owned directory %s: not a real directory", path)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("validate owned directory %s: %w", path, err)
	}
	if err := os.Mkdir(path, mode); err != nil {
		return fmt.Errorf("create owned directory %s: %w", path, err)
	}
	return nil
}

// ValidateAndGuardLease confirms the SVC-1 generation lease is present and
// prevents it from crossing the later exec into opaque upstream Syncthing.
func ValidateAndGuardLease(getenv func(string) string) error {
	if value := getenv("UMRK_SERVICE_LEASE_FD"); value != "3" {
		return fmt.Errorf("leaf-syncthing: UMRK_SERVICE_LEASE_FD=%q, want 3", value)
	}
	if _, err := unix.FcntlInt(ServiceLeaseFD, unix.F_GETFD, 0); err != nil {
		return fmt.Errorf("leaf-syncthing: validate generation lease fd 3: %w", err)
	}
	unix.CloseOnExec(ServiceLeaseFD)
	flags, err := unix.FcntlInt(ServiceLeaseFD, unix.F_GETFD, 0)
	if err != nil {
		return fmt.Errorf("leaf-syncthing: guard generation lease fd 3: %w", err)
	}
	if flags&unix.FD_CLOEXEC == 0 {
		return errors.New("leaf-syncthing: generation lease fd 3 is not close-on-exec")
	}
	return nil
}
