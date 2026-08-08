package uicontrol

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/life1"
)

const fixturesRoot = "../../tests/fixtures/ui-control-v1"

func TestFrozenFixturesRoundTrip(t *testing.T) {
	entries, err := os.ReadDir(fixturesRoot)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		payload, err := os.ReadFile(filepath.Join(fixturesRoot, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		payload = bytes.TrimSpace(payload)
		var roundTrip []byte
		if strings.Contains(entry.Name(), "request") {
			request, err := decodeRequest(payload)
			if err != nil {
				t.Fatalf("%s: %v", entry.Name(), err)
			}
			roundTrip, err = json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
		} else {
			var response Response
			decoder := json.NewDecoder(bytes.NewReader(payload))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&response); err != nil {
				t.Fatalf("%s: %v", entry.Name(), err)
			}
			roundTrip, err = json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
		}
		if !bytes.Equal(roundTrip, payload) {
			t.Fatalf("%s changed after Go round trip\n got: %s\nwant: %s", entry.Name(), roundTrip, payload)
		}
		frame, err := life1.Encode(payload)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := life1.Decode(frame)
		if err != nil || !bytes.Equal(decoded, payload) {
			t.Fatalf("%s framing round trip: %v", entry.Name(), err)
		}
		checked++
	}
	if checked != 7 {
		t.Fatalf("checked %d fixtures, want 7", checked)
	}
}

func TestHandleRejectsProtocolDrift(t *testing.T) {
	status := fixtureStatus()
	tests := []struct {
		request string
		code    string
	}{
		{`{"v":2,"id":"version","op":"status.get","args":{}}`, "unsupported-version"},
		{`{"v":1,"id":"unknown","op":"future.op","args":{}}`, "unsupported-op"},
		{`{"v":1,"id":"args","op":"status.get","args":{"extra":true}}`, "bad-arguments"},
		{`{"v":1,"id":"extra","op":"status.get","args":{},"extra":true}`, "bad-request"},
	}
	for _, test := range tests {
		response := Handle([]byte(test.request), status)
		if response.OK || response.Error == nil || response.Error.Code != test.code {
			t.Fatalf("Handle(%s) = %+v, want %s", test.request, response, test.code)
		}
	}
}

func TestServerOneShotExchangeAndCleanup(t *testing.T) {
	directory := shortTempDir(t)
	socket := filepath.Join(directory, SocketName)
	server, err := Listen(socket, Operations{Status: fixtureStatus})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(socket)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v, error=%v", fileMode(info), err)
	}

	connection, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	request := json.RawMessage(`{"v":1,"id":"server-test","op":"status.get","args":{}}`)
	if err := life1.Write(connection, request); err != nil {
		t.Fatal(err)
	}
	payload, err := life1.Read(connection)
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	var response Response
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.ID != "server-test" || response.Result == nil || response.Result.Upstream.DeviceID == "" {
		t.Fatalf("response = %+v", response)
	}

	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatalf("control socket remained after close: %v", err)
	}
}

func TestServerRefusesRegularFileAtSocketPath(t *testing.T) {
	path := filepath.Join(shortTempDir(t), SocketName)
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if server, err := Listen(path, Operations{Status: fixtureStatus}); err == nil || server != nil {
		t.Fatal("server replaced a non-socket path")
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != "keep" {
		t.Fatalf("protected path changed: %q, %v", contents, err)
	}
}

func TestEnrollCardOperation(t *testing.T) {
	called := ""
	operations := Operations{
		Status: fixtureStatus,
		EnrollCard: func(sourceID string) (Status, *ProtocolError) {
			called = sourceID
			return fixtureStatus(), nil
		},
	}
	response := operations.Handle([]byte(`{"v":1,"id":"enroll","op":"card.enroll","args":{"source_id":"secondary_sd"}}`))
	if !response.OK || called != "secondary_sd" {
		t.Fatalf("enrollment response = %+v, source=%q", response, called)
	}
	response = operations.Handle([]byte(`{"v":1,"id":"enroll","op":"card.enroll","args":{"source_id":"../bad"}}`))
	if response.OK || response.Error == nil || response.Error.Code != "bad-arguments" {
		t.Fatalf("unsafe source response = %+v", response)
	}
}

func TestNetworkProfileOperationRequiresConfirmation(t *testing.T) {
	called := ""
	operations := Operations{
		Status: fixtureStatus,
		SetNetworkProfile: func(profile string) (Status, *ProtocolError) {
			called = profile
			status := fixtureStatus()
			status.Network = &NetworkStatus{Profile: profile, AllowedNetworks: []string{}}
			return status, nil
		},
	}
	response := operations.Handle([]byte(`{"v":1,"id":"network","op":"network.profile.set","args":{"profile":"sync-anywhere","confirmed":true}}`))
	if !response.OK || called != "sync-anywhere" || response.Result == nil || response.Result.Network == nil {
		t.Fatalf("network response = %+v, called=%q", response, called)
	}
	for _, request := range []string{
		`{"v":1,"id":"network","op":"network.profile.set","args":{"profile":"sync-anywhere","confirmed":false}}`,
		`{"v":1,"id":"network","op":"network.profile.set","args":{"profile":"private-addresses","confirmed":true}}`,
	} {
		response = operations.Handle([]byte(request))
		if response.OK || response.Error == nil || response.Error.Code != "bad-arguments" {
			t.Fatalf("unsafe network response = %+v", response)
		}
	}
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "leaf-syncthing-ui-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}

func fixtureStatus() Status {
	return Status{
		Controller: "running",
		Upstream:   UpstreamStatus{State: "running", Version: "v2.1.2", DeviceID: "FIXTURE-DEVICE"},
		Game:       GameStatus{}, Recovery: RecoveryStatus{State: "ready"},
		Capabilities: []string{OperationGet, OperationEnrollCard},
	}
}
