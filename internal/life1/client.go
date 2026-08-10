package life1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	ProtocolVersion    = 1
	DefaultAckMS       = 250
	DefaultWaitMS      = 0
	DefaultCheckWaitMS = 15000
)

type Mode string

const (
	ModeNotify Mode = "notify"
	ModeStop   Mode = "stop"
)

var (
	ErrUnavailable = errors.New("life1: Jawaka unavailable")
	ErrProtocol    = errors.New("life1: protocol error")
	ErrClosed      = errors.New("life1: subscription closed")
	requestCounter atomic.Uint64
)

type Config struct {
	SocketPath      string
	ServiceID       string
	Mode            Mode
	AckMS           int
	WaitMS          int
	CheckBeforeStop bool
	Timeout         time.Duration
}

type ProtocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e ProtocolError) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

type SubscribeRequest struct {
	Version         int      `json:"v"`
	Operation       string   `json:"op"`
	ID              string   `json:"id"`
	Events          []string `json:"events"`
	ServiceID       string   `json:"service_id"`
	Mode            Mode     `json:"mode"`
	AckMS           int      `json:"ack_ms"`
	WaitMS          int      `json:"wait_ms"`
	CheckBeforeStop bool     `json:"check_before_stop,omitempty"`
}

type subscribeResponse struct {
	Version int            `json:"v"`
	ID      string         `json:"id"`
	OK      bool           `json:"ok,omitempty"`
	Error   *ProtocolError `json:"error,omitempty"`
}

type GameStateRequest struct {
	Version   int    `json:"v"`
	Operation string `json:"op"`
	ID        string `json:"id"`
}

type GameState struct {
	Active     bool   `json:"active"`
	LaunchID   string `json:"launch_id,omitempty"`
	SourceID   string `json:"source_id,omitempty"`
	SavesPath  string `json:"saves_path,omitempty"`
	StatesPath string `json:"states_path,omitempty"`
}

type gameStateResponse struct {
	Version    int            `json:"v"`
	ID         string         `json:"id"`
	Active     bool           `json:"active"`
	LaunchID   string         `json:"launch_id,omitempty"`
	SourceID   string         `json:"source_id,omitempty"`
	SavesPath  string         `json:"saves_path,omitempty"`
	StatesPath string         `json:"states_path,omitempty"`
	Error      *ProtocolError `json:"error,omitempty"`
}

type Event struct {
	Version      int    `json:"v"`
	Name         string `json:"event"`
	LaunchID     string `json:"launch_id"`
	SourceID     string `json:"source_id,omitempty"`
	SavesPath    string `json:"saves_path,omitempty"`
	StatesPath   string `json:"states_path,omitempty"`
	WaitBudgetMS *int   `json:"wait_budget_ms,omitempty"`
}

type envelope struct {
	Version int             `json:"v"`
	ID      string          `json:"id"`
	Event   string          `json:"event"`
	Error   json.RawMessage `json:"error"`
}

type Subscription struct {
	conn net.Conn

	readMu  sync.Mutex
	writeMu sync.Mutex
	pending []Event
	closed  bool
}

func ResolveSocket(getenv func(string) string, runtimePath string) (string, error) {
	if explicit := getenv("UMRK_DAEMON_SOCKET"); explicit != "" {
		return filepath.Clean(explicit), nil
	}
	if compatibility := getenv("JAWAKA_SOCKET_PATH"); compatibility != "" {
		return filepath.Clean(compatibility), nil
	}
	if runtimePath == "" {
		return "", fmt.Errorf("%w: UMRK_RUNTIME_PATH is empty", ErrUnavailable)
	}
	return filepath.Join(filepath.Clean(runtimePath), "jawakad.sock"), nil
}

func Connect(ctx context.Context, config Config) (*Subscription, GameState, error) {
	if err := validateConfig(config); err != nil {
		return nil, GameState{}, err
	}
	if config.Timeout == 0 {
		config.Timeout = 2 * time.Second
	}

	dialer := net.Dialer{Timeout: config.Timeout}
	conn, err := dialer.DialContext(ctx, "unix", config.SocketPath)
	if err != nil {
		return nil, GameState{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if err := forceCloseOnExec(conn); err != nil {
		_ = conn.Close()
		return nil, GameState{}, err
	}

	subscription := &Subscription{conn: conn}
	_ = conn.SetDeadline(handshakeDeadline(ctx, config.Timeout))

	subscribeID := requestID()
	request := SubscribeRequest{
		Version: ProtocolVersion, Operation: "subscribe", ID: subscribeID,
		Events: []string{"game"}, ServiceID: config.ServiceID, Mode: config.Mode,
		AckMS: config.AckMS, WaitMS: config.WaitMS,
		CheckBeforeStop: config.CheckBeforeStop,
	}
	if err := subscription.writeJSON(request); err != nil {
		subscription.Close()
		return nil, GameState{}, fmt.Errorf("%w: subscribe: %v", ErrUnavailable, err)
	}
	payload, err := subscription.readResponse(subscribeID)
	if err != nil {
		subscription.Close()
		return nil, GameState{}, err
	}
	var response subscribeResponse
	if err := decodeStrict(payload, &response); err != nil {
		subscription.Close()
		return nil, GameState{}, protocolError("subscribe response", err)
	}
	if response.Version != ProtocolVersion || response.ID != subscribeID {
		subscription.Close()
		return nil, GameState{}, protocolError("subscribe response", errors.New("version or id mismatch"))
	}
	if response.Error != nil {
		if response.OK {
			subscription.Close()
			return nil, GameState{}, protocolError("subscribe response", errors.New("contains both ok and error"))
		}
		subscription.Close()
		return nil, GameState{}, fmt.Errorf("%w: subscribe rejected: %w", ErrProtocol, response.Error)
	}
	if !response.OK {
		subscription.Close()
		return nil, GameState{}, protocolError("subscribe response", errors.New("missing ok=true"))
	}

	stateID := requestID()
	if err := subscription.writeJSON(GameStateRequest{Version: ProtocolVersion, Operation: "game.state", ID: stateID}); err != nil {
		subscription.Close()
		return nil, GameState{}, fmt.Errorf("%w: game.state: %v", ErrUnavailable, err)
	}
	payload, err = subscription.readResponse(stateID)
	if err != nil {
		subscription.Close()
		return nil, GameState{}, err
	}
	var stateResponse gameStateResponse
	if err := decodeStrict(payload, &stateResponse); err != nil {
		subscription.Close()
		return nil, GameState{}, protocolError("game.state response", err)
	}
	if stateResponse.Version != ProtocolVersion || stateResponse.ID != stateID {
		subscription.Close()
		return nil, GameState{}, protocolError("game.state response", errors.New("version or id mismatch"))
	}
	if stateResponse.Error != nil {
		subscription.Close()
		return nil, GameState{}, fmt.Errorf("%w: game.state rejected: %w", ErrProtocol, stateResponse.Error)
	}
	if err := requireJSONFields(payload, "v", "id", "active"); err != nil {
		subscription.Close()
		return nil, GameState{}, protocolError("game.state response", err)
	}
	state := GameState{
		Active: stateResponse.Active, LaunchID: stateResponse.LaunchID,
		SourceID: stateResponse.SourceID, SavesPath: stateResponse.SavesPath,
		StatesPath: stateResponse.StatesPath,
	}
	if err := validateState(state); err != nil {
		subscription.Close()
		return nil, GameState{}, protocolError("game.state response", err)
	}

	_ = conn.SetDeadline(time.Time{})
	return subscription, state, nil
}

func (s *Subscription) Next(ctx context.Context) (Event, error) {
	if s == nil || s.conn == nil {
		return Event{}, ErrClosed
	}
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if len(s.pending) > 0 {
		event := s.pending[0]
		s.pending = s.pending[1:]
		return event, nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return Event{}, err
		}
		deadline := time.Now().Add(500 * time.Millisecond)
		if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
			deadline = contextDeadline
		}
		_ = s.conn.SetReadDeadline(deadline)
		payload, err := Read(s.conn)
		if err != nil {
			var netError net.Error
			if errors.As(err, &netError) && netError.Timeout() {
				continue
			}
			return Event{}, err
		}
		return decodeEvent(payload)
	}
}

func (s *Subscription) SendReady(launchID string) error {
	if launchID == "" {
		return errors.New("life1: ready launch id is empty")
	}
	return s.writeJSON(struct {
		Version  int    `json:"v"`
		Status   string `json:"status"`
		LaunchID string `json:"launch_id"`
	}{ProtocolVersion, "ready", launchID})
}

func (s *Subscription) SendWaiting(launchID string, pendingItems int, pendingBytes int64) error {
	if launchID == "" || pendingItems < 0 || pendingBytes < 0 {
		return errors.New("life1: waiting status requires a launch id and non-negative pending work")
	}
	return s.writeJSON(struct {
		Version      int    `json:"v"`
		Status       string `json:"status"`
		LaunchID     string `json:"launch_id"`
		PendingItems int    `json:"pending_items"`
		PendingBytes int64  `json:"pending_bytes"`
	}{ProtocolVersion, "waiting", launchID, pendingItems, pendingBytes})
}

func (s *Subscription) SendStop(launchID string) error {
	if launchID == "" {
		return errors.New("life1: stop launch id is empty")
	}
	return s.writeJSON(struct {
		Version  int    `json:"v"`
		Status   string `json:"status"`
		LaunchID string `json:"launch_id"`
	}{ProtocolVersion, "stop", launchID})
}

func (s *Subscription) SendError(launchID, reason string) error {
	if launchID == "" || reason == "" {
		return errors.New("life1: error status requires launch id and reason")
	}
	return s.writeJSON(struct {
		Version  int    `json:"v"`
		Status   string `json:"status"`
		LaunchID string `json:"launch_id"`
		Reason   string `json:"reason"`
	}{ProtocolVersion, "error", launchID, reason})
}

func (s *Subscription) Close() error {
	if s == nil || s.conn == nil {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.conn.Close()
}

func (s *Subscription) readResponse(id string) (json.RawMessage, error) {
	for {
		payload, err := Read(s.conn)
		if err != nil {
			return nil, fmt.Errorf("%w: read response: %v", ErrUnavailable, err)
		}
		var header envelope
		if err := json.Unmarshal(payload, &header); err != nil {
			return nil, protocolError("response envelope", err)
		}
		if header.Version != ProtocolVersion {
			return nil, protocolError("response envelope", errors.New("unsupported version"))
		}
		if header.Event != "" {
			event, err := decodeEvent(payload)
			if err != nil {
				return nil, err
			}
			s.pending = append(s.pending, event)
			continue
		}
		if header.ID != id {
			return nil, protocolError("response envelope", fmt.Errorf("unexpected id %q", header.ID))
		}
		return payload, nil
	}
}

func (s *Subscription) writeJSON(message any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed {
		return ErrClosed
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return Write(s.conn, payload)
}

func validateConfig(config Config) error {
	if config.SocketPath == "" || config.ServiceID == "" {
		return errors.New("life1: socket path and service id are required")
	}
	if config.Mode != ModeNotify && config.Mode != ModeStop {
		return fmt.Errorf("life1: invalid mode %q", config.Mode)
	}
	if config.CheckBeforeStop && config.Mode != ModeStop {
		return errors.New("life1: check-before-stop requires stop mode")
	}
	if config.AckMS < 0 || config.WaitMS < 0 {
		return errors.New("life1: ack and wait values must be non-negative")
	}
	if config.Timeout < 0 {
		return errors.New("life1: timeout must be non-negative")
	}
	return nil
}

func validateState(state GameState) error {
	if !state.Active {
		if state.LaunchID != "" || state.SourceID != "" || state.SavesPath != "" || state.StatesPath != "" {
			return errors.New("inactive state carries active launch fields")
		}
		return nil
	}
	if state.LaunchID == "" || (state.SourceID != "primary" && state.SourceID != "secondary_sd") {
		return errors.New("active state omits launch identity or has invalid source")
	}
	return nil
}

func decodeEvent(payload json.RawMessage) (Event, error) {
	var event Event
	if err := decodeStrict(payload, &event); err != nil {
		return Event{}, protocolError("event", err)
	}
	if event.Version != ProtocolVersion || event.LaunchID == "" {
		return Event{}, protocolError("event", errors.New("invalid version or launch id"))
	}
	switch event.Name {
	case "game.start", "game.check":
		if err := requireJSONFields(payload, "v", "event", "launch_id", "source_id", "saves_path", "states_path", "wait_budget_ms"); err != nil {
			return Event{}, protocolError(event.Name, err)
		}
		if event.SourceID != "primary" && event.SourceID != "secondary_sd" {
			return Event{}, protocolError(event.Name, errors.New("invalid source"))
		}
		if event.WaitBudgetMS == nil || *event.WaitBudgetMS < 0 || *event.WaitBudgetMS > 15000 {
			return Event{}, protocolError(event.Name, errors.New("invalid wait budget"))
		}
	case "game.cancel", "game.abort", "game.finish":
		if err := requireJSONFields(payload, "v", "event", "launch_id"); err != nil {
			return Event{}, protocolError(event.Name, err)
		}
		if event.SourceID != "" || event.SavesPath != "" || event.StatesPath != "" || event.WaitBudgetMS != nil {
			return Event{}, protocolError(event.Name, errors.New("unexpected fields"))
		}
	default:
		return Event{}, protocolError("event", fmt.Errorf("unknown event %q", event.Name))
	}
	return event, nil
}

func decodeStrict(payload []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func requireJSONFields(payload []byte, fields ...string) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		return err
	}
	for _, field := range fields {
		if _, ok := object[field]; !ok {
			return fmt.Errorf("missing field %q", field)
		}
	}
	return nil
}

func forceCloseOnExec(conn net.Conn) error {
	syscallConn, ok := conn.(syscall.Conn)
	if !ok {
		return errors.New("life1: Unix connection does not expose its descriptor")
	}
	raw, err := syscallConn.SyscallConn()
	if err != nil {
		return fmt.Errorf("life1: access socket descriptor: %w", err)
	}
	var controlErr error
	if err := raw.Control(func(fd uintptr) {
		unix.CloseOnExec(int(fd))
		flags, flagErr := unix.FcntlInt(fd, unix.F_GETFD, 0)
		if flagErr != nil {
			controlErr = flagErr
		} else if flags&unix.FD_CLOEXEC == 0 {
			controlErr = errors.New("FD_CLOEXEC did not stick")
		}
	}); err != nil {
		return fmt.Errorf("life1: mark socket close-on-exec: %w", err)
	}
	if controlErr != nil {
		return fmt.Errorf("life1: mark socket close-on-exec: %w", controlErr)
	}
	return nil
}

func handshakeDeadline(ctx context.Context, timeout time.Duration) time.Time {
	deadline := time.Now().Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	return deadline
}

func requestID() string {
	return strconv.Itoa(os.Getpid()) + "-" + strconv.FormatUint(requestCounter.Add(1), 36)
}

func protocolError(stage string, err error) error {
	return fmt.Errorf("%w: %s: %v", ErrProtocol, stage, err)
}
