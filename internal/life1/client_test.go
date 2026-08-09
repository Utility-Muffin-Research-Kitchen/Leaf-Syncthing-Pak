package life1

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConnectSubscribesAndQueriesStateOnSameConnection(t *testing.T) {
	socket := shortSocketPath(t)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()

		payload, err := Read(conn)
		if err != nil {
			serverDone <- err
			return
		}
		var subscribe SubscribeRequest
		if err := decodeStrict(payload, &subscribe); err != nil {
			serverDone <- err
			return
		}
		if subscribe.Operation != "subscribe" || subscribe.ServiceID != "org.umrk.syncthing" || subscribe.AckMS != 250 {
			serverDone <- errors.New("unexpected subscribe request")
			return
		}
		if err := writeTestJSON(conn, subscribeResponse{Version: 1, ID: subscribe.ID, OK: true}); err != nil {
			serverDone <- err
			return
		}

		payload, err = Read(conn)
		if err != nil {
			serverDone <- err
			return
		}
		var stateRequest GameStateRequest
		if err := decodeStrict(payload, &stateRequest); err != nil {
			serverDone <- err
			return
		}
		waitBudget := 0
		if err := writeTestJSON(conn, Event{
			Version: 1, Name: "game.start", LaunchID: "launch-race",
			SourceID: "primary", SavesPath: "/card/Saves", StatesPath: "/card/States",
			WaitBudgetMS: &waitBudget,
		}); err != nil {
			serverDone <- err
			return
		}
		if err := writeTestJSON(conn, gameStateResponse{
			Version: 1, ID: stateRequest.ID, Active: true, LaunchID: "launch-race",
			SourceID: "primary", SavesPath: "/card/Saves", StatesPath: "/card/States",
		}); err != nil {
			serverDone <- err
			return
		}

		payload, err = Read(conn)
		if err != nil {
			serverDone <- err
			return
		}
		var ready struct {
			Version  int    `json:"v"`
			Status   string `json:"status"`
			LaunchID string `json:"launch_id"`
		}
		if err := decodeStrict(payload, &ready); err != nil {
			serverDone <- err
			return
		}
		if ready.Status != "ready" || ready.LaunchID != "launch-race" {
			serverDone <- errors.New("unexpected ready response")
			return
		}
		serverDone <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	subscription, state, err := Connect(ctx, Config{
		SocketPath: socket, ServiceID: "org.umrk.syncthing", Mode: ModeNotify,
		AckMS: DefaultAckMS, WaitMS: DefaultWaitMS,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	if !state.Active || state.LaunchID != "launch-race" {
		t.Fatalf("state = %+v", state)
	}
	event, err := subscription.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if event.Name != "game.start" || event.LaunchID != state.LaunchID {
		t.Fatalf("event = %+v", event)
	}
	if err := subscription.SendReady(event.LaunchID); err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestConnectRejectsSubscriptionError(t *testing.T) {
	socket := shortSocketPath(t)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		payload, readErr := Read(conn)
		if readErr != nil {
			return
		}
		var subscribe SubscribeRequest
		if decodeStrict(payload, &subscribe) != nil {
			return
		}
		_ = writeTestJSON(conn, subscribeResponse{
			Version: 1, ID: subscribe.ID,
			Error: &ProtocolError{Code: "stale-generation-peer", Message: "not current"},
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err = Connect(ctx, Config{
		SocketPath: socket, ServiceID: "org.umrk.syncthing", Mode: ModeNotify,
		AckMS: DefaultAckMS,
	})
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("Connect() error = %v, want %v", err, ErrProtocol)
	}
}

func TestDecodeEventRejectsUnknownField(t *testing.T) {
	_, err := decodeEvent(json.RawMessage(`{"v":1,"event":"game.finish","launch_id":"x","surprise":true}`))
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("decodeEvent() error = %v, want %v", err, ErrProtocol)
	}
}

func TestValidateStateFailsClosed(t *testing.T) {
	tests := []GameState{
		{Active: false, LaunchID: "unexpected"},
		{Active: true},
		{Active: true, LaunchID: "launch", SourceID: "unknown"},
	}
	for _, state := range tests {
		if err := validateState(state); err == nil {
			t.Fatalf("validateState(%+v) accepted malformed state", state)
		}
	}
}

func TestDecodeStrictRejectsTrailingJSON(t *testing.T) {
	var request GameStateRequest
	if err := decodeStrict([]byte(`{"v":1,"op":"game.state","id":"7"} {}`), &request); err == nil {
		t.Fatal("decodeStrict accepted two JSON values")
	}
}

func TestResolveSocket(t *testing.T) {
	values := map[string]string{"UMRK_DAEMON_SOCKET": "/tmp/explicit.sock"}
	got, err := ResolveSocket(func(name string) string { return values[name] }, "/tmp/runtime")
	if err != nil || got != "/tmp/explicit.sock" {
		t.Fatalf("ResolveSocket() = %q, %v", got, err)
	}
	delete(values, "UMRK_DAEMON_SOCKET")
	got, err = ResolveSocket(func(name string) string { return values[name] }, "/tmp/runtime")
	if err != nil || got != "/tmp/runtime/jawakad.sock" {
		t.Fatalf("ResolveSocket() = %q, %v", got, err)
	}
}

func writeTestJSON(conn net.Conn, message any) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return Write(conn, payload)
}

func shortSocketPath(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "life1-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return filepath.Join(directory, "socket")
}
