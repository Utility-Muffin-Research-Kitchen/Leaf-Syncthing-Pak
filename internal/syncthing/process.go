package syncthing

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const DefaultReadinessTimeout = 30 * time.Second

// Stop ladder bounds, sized from MLP1 measurements. A settled upstream accepts
// the clean shutdown and exits in 416-1009 ms, or exits on TERM alone in
// 202-634 ms; the windows sit above those ranges so a shutdown that would have
// completed cleanly is not killed for a few hundred milliseconds. An upstream
// still running its start-up scan does not exit inside any plausible window and
// always reaches KILL, so widening further buys nothing for the slow case and
// costs the launch that time. The full ladder is bounded by
// GracefulStopWindow + TermWindow + killVerifyWindow, which must stay far below
// the manifest's stop_grace_ms for the controller — not Jawaka's grace wall —
// to own escalation.
const (
	DefaultGracefulStopWindow = 1200 * time.Millisecond
	DefaultTermWindow         = 600 * time.Millisecond

	shutdownRequestTimeout  = 400 * time.Millisecond
	shutdownRetryDelay      = 100 * time.Millisecond
	shutdownRequestAttempts = 3
	killVerifyWindow        = 2 * time.Second
)

type Conflict struct {
	ProcessIDs       []int
	ConventionalPort bool
}

func (conflict Conflict) Empty() bool {
	return len(conflict.ProcessIDs) == 0 && !conflict.ConventionalPort
}

func (conflict Conflict) Error() string {
	return fmt.Sprintf("foreign Syncthing conflict (pids=%v conventional_gui_port=%v)", conflict.ProcessIDs, conflict.ConventionalPort)
}

type DetectConflictFunc func() (Conflict, error)

type ProcessOptions struct {
	Binary           string
	ConfigDir        string
	DataDir          string
	GUISocket        string
	ReadinessTimeout time.Duration
	DetectConflict   DetectConflictFunc
	Stdout           io.Writer
	Stderr           io.Writer
	// GracefulStopWindow and TermWindow bound the stop ladder's first two
	// rungs. Zero selects the defaults above; tests shorten them.
	GracefulStopWindow time.Duration
	TermWindow         time.Duration
	Logf               func(string, ...any)
}

type Process struct {
	command *exec.Cmd
	done    chan error
	apiKey  string
	client  *http.Client
	options ProcessOptions
	groupID int
}

type gatewayTransport struct {
	base   http.RoundTripper
	apiKey string
}

func (transport gatewayTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Del("Authorization")
	clone.Header.Del("X-API-Key")
	clone.Header.Set("X-API-Key", transport.apiKey)
	return transport.base.RoundTrip(clone)
}

// GatewayTransport keeps the private API key inside the controller-owned
// upstream process while giving the read-only gateway an authenticated Unix
// transport. Client credentials are always removed before key injection.
func (process *Process) GatewayTransport() http.RoundTripper {
	if process == nil || process.client == nil || process.apiKey == "" {
		return nil
	}
	base := process.client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	return gatewayTransport{base: base, apiKey: process.apiKey}
}

func StartProcess(ctx context.Context, options ProcessOptions) (*Process, error) {
	if err := options.validate(); err != nil {
		return nil, err
	}
	detectConflict := options.DetectConflict
	if detectConflict == nil {
		detectConflict = DetectForeignConflict
	}
	conflict, err := detectConflict()
	if err != nil {
		return nil, fmt.Errorf("detect foreign Syncthing: %w", err)
	}
	if !conflict.Empty() {
		return nil, conflict
	}
	if err := removeStaleGUISocket(options.GUISocket); err != nil {
		return nil, err
	}
	configuration, err := readConfig(filepath.Join(options.ConfigDir, "config.xml"))
	if err != nil {
		return nil, fmt.Errorf("read API configuration: %w", err)
	}
	apiKey := strings.TrimSpace(configuration.GUI.APIKey)
	if apiKey == "" {
		return nil, errors.New("start upstream: private API key is empty")
	}
	groupID, err := currentProcessGroup()
	if err != nil {
		return nil, err
	}
	if groupID != os.Getpid() {
		return nil, fmt.Errorf("start upstream: controller pid %d is not supervised group leader %d", os.Getpid(), groupID)
	}

	command := exec.Command(options.Binary,
		"--config="+options.ConfigDir,
		"--data="+options.DataDir,
		"serve",
		"--no-browser",
		"--no-restart",
		"--no-upgrade",
		"--no-port-probing",
		"--gui-address=unix://"+options.GUISocket,
		"--log-file=-")
	configureChild(command)
	command.Stdout = options.Stdout
	command.Stderr = options.Stderr
	if command.Stdout == nil {
		command.Stdout = os.Stdout
	}
	if command.Stderr == nil {
		command.Stderr = os.Stderr
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start pinned Syncthing: %w", err)
	}
	childGroup, err := processGroup(command.Process.Pid)
	if err != nil || childGroup != groupID {
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, fmt.Errorf("start upstream: child process group=%d error=%v, want %d", childGroup, err, groupID)
	}

	transport := &http.Transport{
		DisableCompression: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", options.GUISocket)
		},
	}
	process := &Process{
		command: command, done: make(chan error, 1), apiKey: apiKey,
		client:  &http.Client{Transport: transport, Timeout: time.Second},
		options: options, groupID: groupID,
	}
	go func() {
		process.done <- command.Wait()
		close(process.done)
	}()
	if err := process.waitReady(ctx); err != nil {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cleanupErr := process.TerminateAndVerify(cleanupContext)
		if cleanupErr != nil {
			return nil, fmt.Errorf("upstream readiness failed (%v); cleanup failed: %w", err, cleanupErr)
		}
		return nil, err
	}
	return process, nil
}

func (process *Process) Done() <-chan error {
	return process.done
}

// Shutdown runs the controller-owned stop ladder: request the supported clean
// shutdown, wait a bounded graceful window, then escalate through the guardian
// TERM/KILL path.
//
// The manifest's stop_grace_ms is Jawaka's backstop, not a schedule. Before this
// ladder existed the graceful wait consumed the caller's whole grace, so the
// guardian path below could never run before Jawaka's group SIGKILL at the
// grace wall — a stuck upstream cost the full grace on every launch. The ladder
// must therefore complete well inside stop_grace_ms so that wall stays
// unreachable.
func (process *Process) Shutdown(ctx context.Context) error {
	started := time.Now()
	requested, requestErr := process.requestCleanShutdown(ctx)
	window := process.gracefulStopWindow()
	if requested {
		process.logf("stop: clean shutdown accepted in %d ms; graceful window %d ms",
			elapsedMS(started), window.Milliseconds())
	} else {
		// Nothing accepted the request, so the graceful window has nothing to
		// wait for. Escalate straight away rather than holding the caller for a
		// shutdown upstream was never told to perform.
		process.logf("stop: clean shutdown not accepted after %d ms (%v); escalating immediately",
			elapsedMS(started), requestErr)
		window = 0
	}
	graceful, cancelGraceful := context.WithTimeout(context.Background(), window)
	defer cancelGraceful()
	select {
	case waitErr := <-process.done:
		_ = waitErr
		process.client.CloseIdleConnections()
		// The direct child is gone. A monitor grandchild may still hold the
		// group, so prove absence on a short leash and escalate if it survives.
		absence, cancelAbsence := context.WithTimeout(context.Background(), process.absenceWindow())
		err := waitForGroupAbsence(absence, process.groupID, os.Getpid())
		cancelAbsence()
		if err == nil {
			process.logf("stop: upstream group exited cleanly in %d ms", elapsedMS(started))
			return nil
		}
		process.logf("stop: upstream exited but group survives after %d ms (%s); escalating",
			elapsedMS(started), describeGroup(process.groupID, os.Getpid()))
	case <-graceful.Done():
		if window > 0 {
			process.logf("stop: graceful window elapsed after %d ms (%s); escalating",
				elapsedMS(started), describeGroup(process.groupID, os.Getpid()))
		}
	case <-ctx.Done():
		process.logf("stop: caller grace expired after %d ms (%s); escalating",
			elapsedMS(started), describeGroup(process.groupID, os.Getpid()))
	}
	// The guardian ladder owns its own bounds. It must run to completion even
	// when the caller's grace is already spent, because reporting an unverified
	// stop is worse than overrunning.
	escalation, cancelEscalation := context.WithTimeout(context.Background(), process.escalationWindow())
	defer cancelEscalation()
	err := process.TerminateAndVerify(escalation)
	if err != nil {
		process.logf("stop: group absence unverified after %d ms: %v", elapsedMS(started), err)
		return err
	}
	process.logf("stop: group absence verified after %d ms", elapsedMS(started))
	return nil
}

// requestCleanShutdown asks upstream to shut down through its supported REST
// path. A single attempt shares the client's short timeout, so a busy upstream
// (a scan holding the HTTP handler) can drop the request entirely; retrying
// distinguishes "upstream is busy right now" from "upstream will never answer",
// and the returned error is what the phase log reports.
func (process *Process) requestCleanShutdown(ctx context.Context) (bool, error) {
	var lastErr error
	for attempt := 0; attempt < shutdownRequestAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return false, lastErr
			case <-process.done:
				return true, nil
			case <-time.After(shutdownRetryDelay):
			}
		}
		attemptContext, cancel := context.WithTimeout(context.Background(), shutdownRequestTimeout)
		request, err := http.NewRequestWithContext(
			attemptContext, http.MethodPost, "http://syncthing-unix/rest/system/shutdown", nil)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		request.Header.Set("X-API-Key", process.apiKey)
		response, requestErr := process.client.Do(request)
		if requestErr != nil {
			cancel()
			lastErr = requestErr
			// A missing socket file is terminal, not transient: upstream has
			// already unlinked it on its way down (Jawaka's group TERM usually
			// beats us to it), and it will never reappear for this generation.
			// Retrying only delays escalation.
			if errors.Is(requestErr, fs.ErrNotExist) {
				return false, requestErr
			}
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
		cancel()
		if response.StatusCode >= 400 {
			lastErr = fmt.Errorf("clean shutdown rejected with status %d", response.StatusCode)
			continue
		}
		return true, nil
	}
	if lastErr == nil {
		lastErr = errors.New("clean shutdown request failed")
	}
	return false, lastErr
}

func (process *Process) gracefulStopWindow() time.Duration {
	if process.options.GracefulStopWindow > 0 {
		return process.options.GracefulStopWindow
	}
	return DefaultGracefulStopWindow
}

func (process *Process) termWindow() time.Duration {
	if process.options.TermWindow > 0 {
		return process.options.TermWindow
	}
	return DefaultTermWindow
}

func (process *Process) absenceWindow() time.Duration {
	window := process.termWindow() / 2
	if window < 100*time.Millisecond {
		window = 100 * time.Millisecond
	}
	return window
}

func (process *Process) escalationWindow() time.Duration {
	return process.termWindow() + killVerifyWindow
}

func (process *Process) logf(format string, arguments ...any) {
	if process == nil || process.options.Logf == nil {
		return
	}
	process.options.Logf(format, arguments...)
}

func elapsedMS(since time.Time) int64 {
	return time.Since(since).Milliseconds()
}

// TerminateAndVerify is the guardian cleanup path. The controller stays alive
// while every other member of its reserved group receives TERM/KILL and until
// /proc proves that no non-zombie descendant remains.
func (process *Process) TerminateAndVerify(ctx context.Context) error {
	if process == nil {
		return nil
	}
	if err := signalGroupMembers(process.groupID, os.Getpid(), syscall.SIGTERM); err != nil {
		return err
	}
	termDeadline := time.Now().Add(process.termWindow())

termWait:
	for time.Now().Before(termDeadline) {
		if groupAbsent(process.groupID, os.Getpid()) {
			return nil
		}
		select {
		case <-ctx.Done():
			break termWait
		case <-time.After(25 * time.Millisecond):
		}
	}
	process.logf("stop: group survived TERM window (%s); sending KILL",
		describeGroup(process.groupID, os.Getpid()))
	if err := signalGroupMembers(process.groupID, os.Getpid(), syscall.SIGKILL); err != nil {
		return err
	}
	waitContext := ctx
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		waitContext, cancel = context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
	}
	return waitForGroupAbsence(waitContext, process.groupID, os.Getpid())
}

func (process *Process) waitReady(ctx context.Context) error {
	timeout := process.options.ReadinessTimeout
	if timeout == 0 {
		timeout = DefaultReadinessTimeout
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-process.done:
			return fmt.Errorf("upstream exited before readiness: %w", err)
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("upstream readiness timed out")
		case <-ticker.C:
			ready, err := process.probeReady(ctx)
			if err == nil && ready {
				info, statErr := os.Lstat(process.options.GUISocket)
				if statErr != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
					return fmt.Errorf("private GUI socket mode/type invalid: mode=%v error=%v", infoMode(info), statErr)
				}
				return nil
			}
		}
	}
}

func (process *Process) probeReady(ctx context.Context) (bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://syncthing-unix/rest/system/status", nil)
	if err != nil {
		return false, err
	}
	request.Header.Set("X-API-Key", process.apiKey)
	response, err := process.client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	return response.StatusCode == http.StatusOK, nil
}

func (options ProcessOptions) validate() error {
	if options.Binary == "" || options.ConfigDir == "" || options.DataDir == "" || options.GUISocket == "" {
		return errors.New("start upstream: binary, config, data, and GUI socket are required")
	}
	if options.ReadinessTimeout < 0 || !filepath.IsAbs(options.GUISocket) || filepath.Base(options.GUISocket) != "syncthing-gui.sock" {
		return errors.New("start upstream: readiness timeout or GUI socket is invalid")
	}
	info, err := os.Lstat(options.Binary)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return errors.New("start upstream: binary is not a real executable")
	}
	return nil
}

func removeStaleGUISocket(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.New("start upstream: GUI socket path exists and is not a socket")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale GUI socket: %w", err)
	}
	return nil
}

func infoMode(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode()
}
