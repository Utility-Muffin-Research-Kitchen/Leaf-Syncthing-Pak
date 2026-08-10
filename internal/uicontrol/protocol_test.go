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
	if checked != 20 {
		t.Fatalf("checked %d fixtures, want 20", checked)
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

func TestStatusKeepsRequiredEmptyCollectionsAsArrays(t *testing.T) {
	status := fixtureStatus()
	status.Cards = make([]CardStatus, 0)
	status.Folders = make([]FolderStatus, 0)
	status.Issues = make([]Issue, 0)
	response := Handle([]byte(`{"v":1,"id":"empty","op":"status.get","args":{}}`), status)
	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`"cards":[]`, `"folders":[]`, `"issues":[]`} {
		if !bytes.Contains(payload, []byte(required)) {
			t.Fatalf("required empty collection %s missing from %s", required, payload)
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

func TestGatewayOperationsSeparatePairingFromConfirmedActions(t *testing.T) {
	called := ""
	operations := Operations{
		Status: fixtureStatus,
		GatewayAction: func(operation string) (Status, *ProtocolError) {
			called = operation
			status := fixtureStatus()
			status.Gateway = &GatewayStatus{Open: true, URL: "https://192.0.2.1:8384/", Fingerprint: "AA:BB"}
			return status, nil
		},
	}
	response := operations.Handle([]byte(`{"v":1,"id":"gateway","op":"gateway.open","args":{}}`))
	if !response.OK || called != OperationGatewayOpen {
		t.Fatalf("gateway open = %+v, called=%q", response, called)
	}
	response = operations.Handle([]byte(`{"v":1,"id":"gateway","op":"gateway.revoke-all","args":{"confirmed":false}}`))
	if response.OK || response.Error == nil || response.Error.Code != "bad-arguments" {
		t.Fatalf("unconfirmed revoke = %+v", response)
	}
	response = operations.Handle([]byte(`{"v":1,"id":"gateway","op":"gateway.revoke-all","args":{"confirmed":true}}`))
	if !response.OK || called != OperationGatewayRevoke {
		t.Fatalf("confirmed revoke = %+v, called=%q", response, called)
	}
}

func TestFolderAndDeviceOperationsAreStrict(t *testing.T) {
	var operation, id, name string
	operations := Operations{
		Status: fixtureStatus,
		FolderInspect: func(gotID string) (Status, *ProtocolError) {
			operation, id = OperationFolderInspect, gotID
			return fixtureStatus(), nil
		},
		FolderAction: func(gotOperation, gotID, gotName string) (Status, *ProtocolError) {
			operation, id, name = gotOperation, gotID, gotName
			return fixtureStatus(), nil
		},
		FolderMembership: func(gotOperation, gotID, gotDeviceID string) (Status, *ProtocolError) {
			operation, id, name = gotOperation, gotID, gotDeviceID
			return fixtureStatus(), nil
		},
		DeviceAction: func(gotOperation, gotID, gotName string) (Status, *ProtocolError) {
			operation, id, name = gotOperation, gotID, gotName
			return fixtureStatus(), nil
		},
	}
	response := operations.Handle([]byte(`{"v":1,"id":"folder","op":"folder.rename","args":{"folder_id":"leaf-saves-0011223344556677","label":"My Saves"}}`))
	if !response.OK || operation != OperationFolderRename || id != "leaf-saves-0011223344556677" || name != "My Saves" {
		t.Fatalf("folder rename = %+v, %q %q %q", response, operation, id, name)
	}
	response = operations.Handle([]byte(`{"v":1,"id":"inspect","op":"folder.inspect","args":{"folder_id":"leaf-saves-0011223344556677"}}`))
	if !response.OK || operation != OperationFolderInspect || id != "leaf-saves-0011223344556677" {
		t.Fatalf("folder inspect = %+v, %q %q", response, operation, id)
	}
	peerID := "IIIIIII-JJJJJJJ-KKKKKKK-LLLLLLL-MMMMMMM-NNNNNNN-OOOOOOO-PPPPPPP"
	response = operations.Handle([]byte(`{"v":1,"id":"share","op":"folder.share","args":{"folder_id":"leaf-saves-0011223344556677","device_id":"` + peerID + `","confirmed":true}}`))
	if !response.OK || operation != OperationFolderShare || id != "leaf-saves-0011223344556677" || name != peerID {
		t.Fatalf("folder share = %+v, %q %q %q", response, operation, id, name)
	}
	response = operations.Handle([]byte(`{"v":1,"id":"device","op":"device.add","args":{"device_id":"AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH","name":"Laptop"}}`))
	if !response.OK || operation != OperationDeviceAdd || name != "Laptop" {
		t.Fatalf("device add = %+v, %q %q", response, operation, name)
	}
	response = operations.Handle([]byte(`{"v":1,"id":"device","op":"device.remove","args":{"device_id":"` + peerID + `","confirmed":true}}`))
	if !response.OK || operation != OperationDeviceRemove || id != peerID || name != "" {
		t.Fatalf("device remove = %+v, %q %q %q", response, operation, id, name)
	}
	for _, request := range []string{
		`{"v":1,"id":"folder","op":"folder.pause","args":{"folder_id":"../bad"}}`,
		`{"v":1,"id":"folder","op":"folder.rename","args":{"folder_id":"valid","label":"bad\nname"}}`,
		`{"v":1,"id":"folder","op":"folder.unshare","args":{"folder_id":"valid","device_id":"peer","confirmed":false}}`,
		`{"v":1,"id":"folder","op":"folder.share","args":{"folder_id":"valid","device_id":"peer","confirmed":true,"extra":true}}`,
		`{"v":1,"id":"device","op":"device.add","args":{"device_id":"id","name":"peer","extra":true}}`,
		`{"v":1,"id":"device","op":"device.remove","args":{"device_id":"peer","confirmed":false}}`,
	} {
		response = operations.Handle([]byte(request))
		if response.OK || response.Error == nil || response.Error.Code != "bad-arguments" {
			t.Fatalf("unsafe action accepted: %s = %+v", request, response)
		}
	}
}

func TestFolderOnboardingAndFirstSyncOperationsRequireExplicitAcknowledgments(t *testing.T) {
	called := ""
	selectedPeer := "IIIIIII-JJJJJJJ-KKKKKKK-LLLLLLL-MMMMMMM-NNNNNNN-OOOOOOO-PPPPPPP"
	operations := Operations{
		Status: fixtureStatus,
		PlanFolder: func(sourceID, kind, folderType string, deviceIDs []string) (Status, *ProtocolError) {
			if len(deviceIDs) != 1 || deviceIDs[0] != selectedPeer {
				t.Fatalf("onboarding selected peers = %v", deviceIDs)
			}
			called = sourceID + ":" + kind + ":" + folderType
			status := fixtureStatus()
			status.Onboarding = &OnboardingStatus{
				PlanID: "00112233445566778899aabbccddeeff", SourceID: sourceID,
				CardID: "ffeeddccbbaa99887766554433221100", Kind: kind, FolderType: folderType,
				FolderID: "leaf-" + kind + "-0011223344556677", Label: "Leaf Saves", Path: "/card/Saves",
				FileCount: 2, DirectoryCount: 1, ContentBytes: 10, AvailableBytes: 100,
				SnapshotPossible: true, PeerCount: 1, ExpiresAt: "2026-08-09T12:05:00Z",
			}
			return status, nil
		},
		PlanFolderOffer: func(folderID, deviceID, sourceID, kind, folderType string) (Status, *ProtocolError) {
			called = folderID + ":" + sourceID + ":" + kind + ":" + folderType
			status := fixtureStatus()
			status.Onboarding = &OnboardingStatus{
				PlanID: "00112233445566778899aabbccddeeff", SourceID: sourceID,
				CardID: "ffeeddccbbaa99887766554433221100", Kind: kind, FolderType: folderType,
				FolderID: folderID, Label: "Retro Saves", Path: "/card/Saves",
				FileCount: 2, DirectoryCount: 1, ContentBytes: 10, AvailableBytes: 100,
				SnapshotPossible: true, PeerCount: 1, JoinExisting: true, OfferDeviceID: deviceID,
				ExpiresAt: "2026-08-09T12:05:00Z",
			}
			return status, nil
		},
		CreateFolder: func(planID string, statesAcknowledged, manualAcknowledged bool) (Status, *ProtocolError) {
			called = planID
			if statesAcknowledged || !manualAcknowledged {
				t.Fatalf("create acknowledgments = states %v manual %v", statesAcknowledged, manualAcknowledged)
			}
			return fixtureStatus(), nil
		},
		PrepareFirstSync: func(folderID string) (Status, *ProtocolError) {
			called = "prepare:" + folderID
			return fixtureStatus(), nil
		},
		StartFirstSync: func(folderID string, hubAcknowledged bool) (Status, *ProtocolError) {
			called = "start:" + folderID
			if !hubAcknowledged {
				t.Fatal("hub acknowledgment was not forwarded")
			}
			return fixtureStatus(), nil
		},
		SetFolderType: func(folderID, folderType string) (Status, *ProtocolError) {
			called = folderID + ":" + folderType
			return fixtureStatus(), nil
		},
	}
	response := operations.Handle([]byte(`{"v":1,"id":"plan","op":"folder.onboard.plan","args":{"source_id":"primary","kind":"saves","folder_type":"sendreceive","device_ids":["` + selectedPeer + `"]}}`))
	if !response.OK || called != "primary:saves:sendreceive" || response.Result == nil || response.Result.Onboarding == nil {
		t.Fatalf("onboarding plan = %+v, called=%q", response, called)
	}
	offerDeviceID := selectedPeer
	response = operations.Handle([]byte(`{"v":1,"id":"offer","op":"folder.offer.plan","args":{"folder_id":"retro-saves","device_id":"` + offerDeviceID + `","source_id":"primary","kind":"saves","folder_type":"sendreceive"}}`))
	if !response.OK || called != "retro-saves:primary:saves:sendreceive" || response.Result == nil ||
		response.Result.Onboarding == nil || !response.Result.Onboarding.JoinExisting || response.Result.Onboarding.OfferDeviceID != offerDeviceID {
		t.Fatalf("folder offer plan = %+v, called=%q", response, called)
	}
	response = operations.Handle([]byte(`{"v":1,"id":"create","op":"folder.onboard.create","args":{"plan_id":"00112233445566778899aabbccddeeff","confirmed":true,"states_warning_acknowledged":false,"manual_edit_warning_acknowledged":true}}`))
	if !response.OK || called != "00112233445566778899aabbccddeeff" {
		t.Fatalf("onboarding create = %+v, called=%q", response, called)
	}
	response = operations.Handle([]byte(`{"v":1,"id":"prepare","op":"folder.first-sync.prepare","args":{"folder_id":"leaf-saves-0011223344556677","confirmed":true,"snapshot_limit_acknowledged":true}}`))
	if !response.OK || called != "prepare:leaf-saves-0011223344556677" {
		t.Fatalf("first-sync prepare = %+v, called=%q", response, called)
	}
	response = operations.Handle([]byte(`{"v":1,"id":"start","op":"folder.first-sync.start","args":{"folder_id":"leaf-saves-0011223344556677","confirmed":true,"hub_versioning_acknowledged":true}}`))
	if !response.OK || called != "start:leaf-saves-0011223344556677" {
		t.Fatalf("first-sync start = %+v, called=%q", response, called)
	}
	response = operations.Handle([]byte(`{"v":1,"id":"type","op":"folder.type.set","args":{"folder_id":"leaf-saves-0011223344556677","folder_type":"sendonly","confirmed":true}}`))
	if !response.OK || called != "leaf-saves-0011223344556677:sendonly" {
		t.Fatalf("folder type = %+v, called=%q", response, called)
	}

	for _, request := range []string{
		`{"v":1,"id":"plan","op":"folder.onboard.plan","args":{"source_id":"primary","kind":"saves","folder_type":"sendreceive","device_ids":[]}}`,
		`{"v":1,"id":"plan","op":"folder.onboard.plan","args":{"source_id":"primary","kind":"saves","folder_type":"sendreceive"}}`,
		`{"v":1,"id":"plan","op":"folder.onboard.plan","args":{"source_id":"primary","kind":"roms","folder_type":"sendreceive"}}`,
		`{"v":1,"id":"offer","op":"folder.offer.plan","args":{"folder_id":"../bad","device_id":"peer","source_id":"primary","kind":"saves","folder_type":"sendreceive"}}`,
		`{"v":1,"id":"create","op":"folder.onboard.create","args":{"plan_id":"00112233445566778899aabbccddeeff","confirmed":true,"states_warning_acknowledged":false,"manual_edit_warning_acknowledged":false}}`,
		`{"v":1,"id":"prepare","op":"folder.first-sync.prepare","args":{"folder_id":"leaf-saves-0011223344556677","confirmed":true,"snapshot_limit_acknowledged":false}}`,
		`{"v":1,"id":"start","op":"folder.first-sync.start","args":{"folder_id":"leaf-saves-0011223344556677","confirmed":true,"hub_versioning_acknowledged":false}}`,
		`{"v":1,"id":"type","op":"folder.type.set","args":{"folder_id":"leaf-saves-0011223344556677","folder_type":"receiveencrypted","confirmed":true}}`,
	} {
		response = operations.Handle([]byte(request))
		if response.OK || response.Error == nil || response.Error.Code != "bad-arguments" {
			t.Fatalf("unsafe B3 action accepted: %s = %+v", request, response)
		}
	}
}

func TestFolderOfferActionsRequireConfirmation(t *testing.T) {
	called := ""
	operations := Operations{
		Status: fixtureStatus,
		FolderOfferAction: func(operation, folderID, deviceID string) (Status, *ProtocolError) {
			called = operation + ":" + folderID + ":" + deviceID
			return fixtureStatus(), nil
		},
	}
	for _, operation := range []string{OperationFolderOfferIgnore, OperationFolderOfferRestore} {
		response := operations.Handle([]byte(`{"v":1,"id":"offer","op":"` + operation + `","args":{"folder_id":"retro-saves","device_id":"peer","confirmed":true}}`))
		if !response.OK || called != operation+":retro-saves:peer" {
			t.Fatalf("%s = %+v, called=%q", operation, response, called)
		}
	}
	response := operations.Handle([]byte(`{"v":1,"id":"offer","op":"folder.offer.ignore","args":{"folder_id":"retro-saves","device_id":"peer","confirmed":false}}`))
	if response.OK || response.Error == nil || response.Error.Code != "bad-arguments" {
		t.Fatalf("unconfirmed ignore = %+v", response)
	}
}

func TestResetPreparationRequiresExactStrongConfirmation(t *testing.T) {
	called := ""
	operations := Operations{
		Status: fixtureStatus,
		PrepareReset: func(action string) (Status, *ProtocolError) {
			called = action
			status := fixtureStatus()
			status.Recovery.PlanID = "00112233445566778899aabbccddeeff"
			status.Recovery.PlanAction = action
			return status, nil
		},
	}
	response := operations.Handle([]byte(`{"v":1,"id":"reset","op":"reset.prepare","args":{"action":"full","confirmed":true,"confirmation":"RESET SYNCTHING"}}`))
	if !response.OK || called != "full" {
		t.Fatalf("confirmed reset = %+v, called=%q", response, called)
	}
	for _, request := range []string{
		`{"v":1,"id":"reset","op":"reset.prepare","args":{"action":"full","confirmed":false,"confirmation":"RESET SYNCTHING"}}`,
		`{"v":1,"id":"reset","op":"reset.prepare","args":{"action":"full","confirmed":true,"confirmation":"reset syncthing"}}`,
		`{"v":1,"id":"reset","op":"reset.prepare","args":{"action":"everything","confirmed":true,"confirmation":"RESET SYNCTHING"}}`,
	} {
		response = operations.Handle([]byte(request))
		if response.OK || response.Error == nil || response.Error.Code != "bad-arguments" {
			t.Fatalf("unsafe reset accepted: %s = %+v", request, response)
		}
	}
}

func TestLoggingAndDiagnosticsOperationsAreStrict(t *testing.T) {
	level := ""
	exported := false
	operations := Operations{
		Status: fixtureStatus,
		SetLogLevel: func(got string) (Status, *ProtocolError) {
			level = got
			status := fixtureStatus()
			status.Logging = &LoggingStatus{Level: got}
			return status, nil
		},
		ExportDiagnostics: func() (Status, *ProtocolError) {
			exported = true
			status := fixtureStatus()
			status.Diagnostics = &DiagnosticsStatus{LastExportPath: "/logs/leaf-syncthing-diagnostics.json"}
			return status, nil
		},
	}
	response := operations.Handle([]byte(`{"v":1,"id":"log","op":"log.level.set","args":{"level":"debug","confirmed":true}}`))
	if !response.OK || level != "debug" || response.Result == nil || response.Result.Logging == nil {
		t.Fatalf("logging response = %+v, level=%q", response, level)
	}
	for _, request := range []string{
		`{"v":1,"id":"log","op":"log.level.set","args":{"level":"debug","confirmed":false}}`,
		`{"v":1,"id":"log","op":"log.level.set","args":{"level":"trace","confirmed":true}}`,
	} {
		response = operations.Handle([]byte(request))
		if response.OK || response.Error == nil || response.Error.Code != "bad-arguments" {
			t.Fatalf("unsafe log request accepted: %s = %+v", request, response)
		}
	}
	response = operations.Handle([]byte(`{"v":1,"id":"diagnostics","op":"diagnostics.export","args":{}}`))
	if !response.OK || !exported || response.Result == nil || response.Result.Diagnostics == nil {
		t.Fatalf("diagnostics response = %+v, exported=%v", response, exported)
	}
	response = operations.Handle([]byte(`{"v":1,"id":"diagnostics","op":"diagnostics.export","args":{"path":"/tmp/out"}}`))
	if response.OK || response.Error == nil || response.Error.Code != "bad-arguments" {
		t.Fatalf("diagnostics accepted caller-selected path: %+v", response)
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
