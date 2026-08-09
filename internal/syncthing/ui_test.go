package syncthing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type uiRoundTripFunc func(*http.Request) (*http.Response, error)

func (function uiRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestReadUIStatusReturnsBoundedFoldersPeersAndTransfer(t *testing.T) {
	transport := roundTripMap(t, map[string]string{
		"GET /rest/config/devices":                               `[{"deviceID":"SELF","name":"Leaf"},{"deviceID":"AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH","name":"Laptop"}]`,
		"GET /rest/system/connections":                           `{"connections":{"AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH":{"connected":true,"address":"tcp://192.0.2.2:22000","type":"tcp-client","isLocal":true,"inBytesTotal":10,"outBytesTotal":20}}}`,
		"GET /rest/cluster/pending/devices":                      `{"IIIIIII-JJJJJJJ-KKKKKKK-LLLLLLL-MMMMMMM-NNNNNNN-OOOOOOO-PPPPPPP":{"name":"Introduced"}}`,
		"GET /rest/cluster/pending/folders":                      `{"retro-saves":{"offeredBy":{"AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH":{"time":"2026-08-09T12:34:56.123Z","label":"Retro Saves","receiveEncrypted":false,"remoteEncrypted":false}}}}`,
		"GET /rest/stats/folder":                                 `{"leaf-saves-0011223344556677":{"lastFile":{"at":"2026-08-08T10:00:00Z"}}}`,
		"GET /rest/db/status?folder=leaf-saves-0011223344556677": `{"state":"syncing","localBytes":100,"globalBytes":140,"needBytes":40,"errors":0,"pullErrors":0}`,
	})
	process := &Process{client: &http.Client{Transport: transport}, apiKey: "secret"}
	status, err := process.ReadUIStatus(context.Background(), []ConfiguredFolder{{ID: "leaf-saves-0011223344556677"}}, "SELF")
	if err != nil {
		t.Fatal(err)
	}
	if status.Transfer.State != "syncing" || status.Transfer.NeedBytes != 40 || len(status.Peers) != 2 ||
		status.Peers[0].State != "pending" || status.Peers[1].Connection != "local" ||
		status.Folders["leaf-saves-0011223344556677"].LastActivity != "2026-08-08T10:00:00Z" ||
		len(status.FolderOffers) != 1 || status.FolderOffers[0].FolderID != "retro-saves" ||
		status.FolderOffers[0].DeviceName != "Laptop" || status.FolderOffers[0].OfferedAt != "2026-08-09T12:34:56Z" {
		t.Fatalf("UI status = %+v", status)
	}
}

func TestUIActionsValidateAndConstrainUpstreamMutations(t *testing.T) {
	var requests []*http.Request
	transport := uiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.Clone(request.Context()))
		if request.Body != nil {
			payload, _ := io.ReadAll(request.Body)
			requests[len(requests)-1].Body = io.NopCloser(strings.NewReader(string(payload)))
		}
		return &http.Response{StatusCode: http.StatusNoContent, Header: http.Header{}, Body: http.NoBody, Request: request}, nil
	})
	process := &Process{client: &http.Client{Transport: transport}, apiKey: "secret"}
	folderID := "retro-saves"
	deviceID := "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"
	if err := process.SetFolderPaused(context.Background(), folderID, true); err != nil {
		t.Fatal(err)
	}
	if err := process.RescanFolder(context.Background(), folderID); err != nil {
		t.Fatal(err)
	}
	if err := process.RenameFolder(context.Background(), folderID, "Leaf Saves"); err != nil {
		t.Fatal(err)
	}
	if err := process.AddPeer(context.Background(), "syncthing://"+deviceID, "Laptop", []string{"192.0.2.0/24"}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 4 || requests[0].Method != http.MethodPatch || requests[1].Method != http.MethodPost ||
		requests[3].URL.Path != "/rest/config/devices" {
		t.Fatalf("requests = %+v", requests)
	}
	peerPayload, _ := io.ReadAll(requests[3].Body)
	var peer map[string]any
	if err := json.Unmarshal(peerPayload, &peer); err != nil || peer["autoAcceptFolders"] != false || peer["introducer"] != false {
		t.Fatalf("peer payload = %s, %v", peerPayload, err)
	}
	if err := process.AddPeer(context.Background(), "not-a-device", "Bad", nil); err == nil {
		t.Fatal("invalid peer id was accepted")
	}
}

func roundTripMap(t *testing.T, responses map[string]string) http.RoundTripper {
	t.Helper()
	return uiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		key := request.Method + " " + request.URL.RequestURI()
		payload, ok := responses[key]
		if !ok {
			t.Fatalf("unexpected upstream request %s", key)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(payload)), Request: request}, nil
	})
}
