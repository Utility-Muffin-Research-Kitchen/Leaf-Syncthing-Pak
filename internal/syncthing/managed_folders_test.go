package syncthing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

const (
	managedSelf = "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"
	managedPeer = "IIIIIII-JJJJJJJ-KKKKKKK-LLLLLLL-MMMMMMM-NNNNNNN-OOOOOOO-PPPPPPP"
)

func TestConfiguredFolderDevicesRequiresSelfAndRemotePeer(t *testing.T) {
	process := &Process{client: &http.Client{Transport: roundTripMap(t, map[string]string{
		"GET /rest/config/devices": `[{"deviceID":"` + managedPeer + `"},{"deviceID":"` + managedSelf + `"}]`,
	})}, apiKey: "secret"}
	devices, err := process.ConfiguredFolderDevices(context.Background(), managedSelf)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 || devices[0] != managedSelf || devices[1] != managedPeer {
		t.Fatalf("folder devices = %v", devices)
	}

	process.client.Transport = roundTripMap(t, map[string]string{
		"GET /rest/config/devices": `[{"deviceID":"` + managedSelf + `"}]`,
	})
	if _, err := process.ConfiguredFolderDevices(context.Background(), managedSelf); err == nil || !strings.Contains(err.Error(), "at least one") {
		t.Fatalf("single-device error = %v", err)
	}
}

func TestManagedFolderMutationsArePausedBoundedAndVersioned(t *testing.T) {
	type capturedRequest struct {
		method string
		path   string
		body   []byte
	}
	requests := []capturedRequest{}
	transport := uiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		payload, _ := io.ReadAll(request.Body)
		requests = append(requests, capturedRequest{method: request.Method, path: request.URL.Path, body: payload})
		return &http.Response{StatusCode: http.StatusNoContent, Header: http.Header{}, Body: http.NoBody, Request: request}, nil
	})
	process := &Process{client: &http.Client{Transport: transport}, apiKey: "secret"}
	root := t.TempDir()
	folder := ConfiguredFolder{
		ID: "retro-saves", Label: "Leaf Saves", Kind: "saves",
		Path: filepath.Join(root, "Saves"), Type: "sendreceive", MarkerName: ".leaf-saves-001122334455",
		VersioningType: "simple", VersioningFSPath: filepath.Join(root, ".userdata", "mlp1", "Syncthing", "versions", "saves"),
		VersioningFSType: "basic", Devices: []string{managedSelf, managedPeer},
	}
	if err := process.AddManagedFolder(context.Background(), folder); err != nil {
		t.Fatal(err)
	}
	folder.Type = "receiveonly"
	if err := process.SetManagedFolderType(context.Background(), folder); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0].method != http.MethodPost || requests[0].path != "/rest/config/folders" ||
		requests[1].method != http.MethodPatch || requests[1].path != "/rest/config/folders/retro-saves" {
		t.Fatalf("requests = %+v", requests)
	}
	var create managedFolderRequest
	if err := json.Unmarshal(requests[0].body, &create); err != nil {
		t.Fatal(err)
	}
	if !create.Paused || !create.IgnorePerms || create.FilesystemType != "basic" || create.Versioning == nil ||
		create.Versioning.Type != "simple" || create.Versioning.Params["keep"] != "5" ||
		create.Versioning.FSPath != folder.VersioningFSPath || len(create.Devices) != 2 {
		t.Fatalf("create payload = %+v", create)
	}
	var patch map[string]json.RawMessage
	if err := json.Unmarshal(requests[1].body, &patch); err != nil {
		t.Fatal(err)
	}
	if string(patch["paused"]) != "true" || string(patch["type"]) != `"receiveonly"` || patch["versioning"] == nil {
		t.Fatalf("type patch = %s", requests[1].body)
	}
}

func TestManagedFolderMutationRejectsUnsafeInputBeforeAPI(t *testing.T) {
	calls := 0
	process := &Process{client: &http.Client{Transport: uiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return nil, nil
	})}, apiKey: "secret"}
	base := ConfiguredFolder{
		ID: "leaf-saves-0011223344556677", Label: "Leaf Saves", Path: filepath.Join(t.TempDir(), "Saves"),
		Type: "sendonly", MarkerName: ".leaf-saves-001122334455", Devices: []string{managedSelf, managedPeer},
	}
	for _, mutate := range []func(*ConfiguredFolder){
		func(folder *ConfiguredFolder) { folder.Path = "relative" },
		func(folder *ConfiguredFolder) { folder.MarkerName = ".stfolder" },
		func(folder *ConfiguredFolder) { folder.Type = "receiveencrypted" },
		func(folder *ConfiguredFolder) { folder.Devices = []string{managedSelf} },
		func(folder *ConfiguredFolder) { folder.Devices[1] = folder.Devices[0] },
		func(folder *ConfiguredFolder) {
			folder.Type = "sendreceive"
			folder.VersioningType = "none"
		},
	} {
		folder := base
		folder.Devices = append([]string(nil), base.Devices...)
		mutate(&folder)
		if err := process.AddManagedFolder(context.Background(), folder); err == nil {
			t.Fatalf("unsafe folder accepted: %+v", folder)
		}
	}
	if calls != 0 {
		t.Fatalf("unsafe mutations reached API %d times", calls)
	}
}
