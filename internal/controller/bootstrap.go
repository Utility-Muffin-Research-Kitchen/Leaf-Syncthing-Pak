// Package controller owns the resident Leaf Syncthing service process.
package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

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
	ErrB1Incomplete   = errors.New("leaf-syncthing: B1 upstream startup is not implemented yet")
)

type Config struct {
	RuntimeDir      string
	UserdataPath    string
	ConfigDir       string
	DataDir         string
	UpstreamBinary  string
	UpstreamVersion string
	GUISocket       string
	DaemonSocket    string
	Mode            life1.Mode
	AckMS           int
	WaitMS          int
	RetryDelay      time.Duration
}

type Lifecycle interface {
	Close() error
}

type ConnectFunc func(context.Context, life1.Config) (Lifecycle, life1.GameState, error)
type RecoverConfigFunc func(string, syncthingconfig.SyncFilesystemFunc) (syncthingconfig.RecoveryResult, error)
type EnsureIdentityFunc func(context.Context, syncthingconfig.IdentityOptions, syncthingconfig.RecoveryResult) (syncthingconfig.Identity, error)

type Runner struct {
	Config         Config
	Connect        ConnectFunc
	Recover        RecoverConfigFunc
	EnsureIdentity EnsureIdentityFunc
	Logf           func(string, ...any)
}

type Session struct {
	State     life1.GameState
	Recovery  syncthingconfig.RecoveryResult
	Identity  syncthingconfig.Identity
	Lifecycle Lifecycle
	lock      *os.File
}

func LoadConfig() (Config, error) {
	environment, err := leaf.LoadEnvironment()
	if err != nil {
		return Config{}, err
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
		ConfigDir:       filepath.Join(environment.StateDir(), "config"),
		DataDir:         filepath.Join(environment.StateDir(), "data"),
		UpstreamBinary:  filepath.Join(filepath.Dir(executable), "syncthing"),
		UpstreamVersion: PinnedUpstreamVersion,
		GUISocket:       filepath.Join(environment.RuntimePath, "services", ServiceDirName, "syncthing-gui.sock"),
		DaemonSocket:    socket,
		Mode:            life1.ModeNotify,
		AckMS:           life1.DefaultAckMS,
		WaitMS:          life1.DefaultWaitMS,
		RetryDelay:      DefaultRetry,
	}, nil
}

// Bootstrap implements the first five normative SYNC-1 startup steps. It
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

	if err := prepareDurableDirectories(runner.Config.UserdataPath); err != nil {
		return nil, err
	}

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

	var lifecycle Lifecycle
	var state life1.GameState
	for {
		lifecycle, state, err = connect(ctx, life1.Config{
			SocketPath: runner.Config.DaemonSocket,
			ServiceID:  ServiceID,
			Mode:       runner.Config.Mode,
			AckMS:      runner.Config.AckMS,
			WaitMS:     runner.Config.WaitMS,
		})
		if err == nil {
			break
		}
		if !errors.Is(err, life1.ErrUnavailable) {
			return nil, fmt.Errorf("establish LIFE-1 subscription: %w", err)
		}
		if runner.Logf != nil {
			runner.Logf("Jawaka unavailable; upstream remains stopped: %v", err)
		}
		timer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
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

	closeLock = false
	return &Session{State: state, Recovery: recovery, Identity: identity, Lifecycle: lifecycle, lock: lock}, nil
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
	if config.RuntimeDir == "" || config.UserdataPath == "" || config.ConfigDir == "" || config.DataDir == "" ||
		config.UpstreamBinary == "" || config.UpstreamVersion == "" || config.GUISocket == "" || config.DaemonSocket == "" {
		return errors.New("leaf-syncthing: runtime, userdata, config, data, upstream, and daemon values are required")
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
