//go:build linux

package syncthing

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// The stop ladder must own escalation inside the caller's grace. These tests
// pin the property that regressed in qualification: a stubborn upstream cost
// the entire stop_grace_ms because the graceful wait consumed it, leaving
// Jawaka's group SIGKILL at the grace wall as the only thing that ever ran.

type ladderFixture struct {
	process   *Process
	child     *exec.Cmd
	shutdowns *atomic.Int32
	logs      *logRecorder
}

type logRecorder struct {
	mutex sync.Mutex
	lines []string
}

func (recorder *logRecorder) logf(format string, arguments ...any) {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	recorder.lines = append(recorder.lines, fmt.Sprintf(format, arguments...))
}

func (recorder *logRecorder) contains(fragment string) bool {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	for _, line := range recorder.lines {
		if strings.Contains(line, fragment) {
			return true
		}
	}
	return false
}

// newLadderFixture starts a child in its own process group so the ladder's
// group signals can never reach the test runner, and points the REST client at
// a controllable stub of upstream's shutdown endpoint.
func newLadderFixture(t *testing.T, script string, serveREST bool, honorShutdown bool) *ladderFixture {
	t.Helper()
	shutdowns := &atomic.Int32{}
	child := exec.Command("/bin/sh", "-c", script)
	// Setpgid isolates the group under test. Without it signalGroupMembers
	// would target the test runner's own group.
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	groupID := child.Process.Pid
	done := make(chan error, 1)
	go func() {
		done <- child.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = syscall.Kill(-groupID, syscall.SIGKILL)
		<-done
	})

	socketPath := filepath.Join(t.TempDir(), "gui.sock")
	if serveREST {
		listener, err := net.Listen("unix", socketPath)
		if err != nil {
			t.Fatal(err)
		}
		server := &http.Server{Handler: http.HandlerFunc(
			func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/rest/system/shutdown" {
					writer.WriteHeader(http.StatusNotFound)
					return
				}
				shutdowns.Add(1)
				writer.WriteHeader(http.StatusOK)
				if honorShutdown {
					// Mirror upstream: accept, then wind down the process.
					go func() { _ = syscall.Kill(-groupID, syscall.SIGTERM) }()
				}
			})}
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(func() { _ = server.Close() })
	}

	recorder := &logRecorder{}
	transport := &http.Transport{DisableCompression: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		}}
	process := &Process{
		command: child, done: done, apiKey: "fixture-api-key",
		client:  &http.Client{Transport: transport, Timeout: time.Second},
		groupID: groupID,
		options: ProcessOptions{
			GracefulStopWindow: 400 * time.Millisecond,
			TermWindow:         200 * time.Millisecond,
			Logf:               recorder.logf,
		},
	}
	return &ladderFixture{process: process, child: child, shutdowns: shutdowns, logs: recorder}
}

// A stubborn upstream that ignores both the clean shutdown and SIGTERM must be
// killed by the controller's own ladder, not by the caller's grace expiring.
func TestShutdownEscalatesWithoutConsumingCallerGrace(t *testing.T) {
	fixture := newLadderFixture(t,
		`trap "" TERM; while :; do sleep 0.05; done`, true, false)
	grace := 10 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()

	started := time.Now()
	if err := fixture.process.Shutdown(ctx); err != nil {
		t.Fatalf("stubborn upstream stop = %v", err)
	}
	elapsed := time.Since(started)

	// Ladder bound is GracefulStopWindow + TermWindow plus verification slack;
	// anything near the caller's grace means the ladder never ran.
	if elapsed > 3*time.Second {
		t.Fatalf("stop took %s, want the bounded ladder well under the %s grace", elapsed, grace)
	}
	if fixture.shutdowns.Load() == 0 {
		t.Fatal("clean shutdown was never requested before escalation")
	}
	if !groupAbsent(fixture.process.groupID, os.Getpid()) {
		t.Fatal("group survived a completed stop")
	}
	if !fixture.logs.contains("sending KILL") {
		t.Fatalf("phase log did not record escalation: %v", fixture.logs.lines)
	}
}

// The clean path must stay clean: a cooperative upstream exits inside the
// graceful window and is never signalled by the guardian ladder.
func TestShutdownCooperativeUpstreamSkipsEscalation(t *testing.T) {
	fixture := newLadderFixture(t, `while :; do sleep 0.05; done`, true, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	started := time.Now()
	if err := fixture.process.Shutdown(ctx); err != nil {
		t.Fatalf("cooperative upstream stop = %v", err)
	}
	if elapsed := time.Since(started); elapsed > fixture.process.gracefulStopWindow() {
		t.Fatalf("cooperative stop took %s, want under the graceful window", elapsed)
	}
	if fixture.logs.contains("sending KILL") {
		t.Fatalf("cooperative stop escalated to KILL: %v", fixture.logs.lines)
	}
	if !fixture.logs.contains("exited cleanly") {
		t.Fatalf("phase log did not record the clean path: %v", fixture.logs.lines)
	}
}

// When the REST endpoint never accepts the request there is nothing to wait
// for, so the graceful window must be skipped rather than served in full.
func TestShutdownSkipsGracefulWindowWhenRequestUnaccepted(t *testing.T) {
	fixture := newLadderFixture(t,
		`trap "" TERM; while :; do sleep 0.05; done`, false, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	started := time.Now()
	if err := fixture.process.Shutdown(ctx); err != nil {
		t.Fatalf("unreachable-endpoint stop = %v", err)
	}
	elapsed := time.Since(started)
	// Request attempts plus the TERM window, but not the graceful window.
	if elapsed > 2*time.Second {
		t.Fatalf("stop took %s, want prompt escalation", elapsed)
	}
	// A missing socket is terminal, so the retry delays must not be served:
	// upstream unlinked it going down and it cannot come back this generation.
	if elapsed > shutdownRetryDelay*shutdownRequestAttempts+fixture.process.termWindow()+time.Second {
		t.Fatalf("stop took %s, want the retry loop short-circuited on ENOENT", elapsed)
	}
	if !fixture.logs.contains("not accepted") {
		t.Fatalf("phase log did not record the failed request: %v", fixture.logs.lines)
	}
	if !groupAbsent(fixture.process.groupID, os.Getpid()) {
		t.Fatal("group survived a completed stop")
	}
}

// An already-expired caller grace must still produce a verified stop; the
// guardian ladder runs on its own bounds because reporting an unverified stop
// is worse than overrunning.
func TestShutdownVerifiesEvenWhenCallerGraceExpired(t *testing.T) {
	fixture := newLadderFixture(t,
		`trap "" TERM; while :; do sleep 0.05; done`, true, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := fixture.process.Shutdown(ctx); err != nil {
		t.Fatalf("expired-grace stop = %v", err)
	}
	if !groupAbsent(fixture.process.groupID, os.Getpid()) {
		t.Fatal("group survived a stop reported as verified")
	}
}
