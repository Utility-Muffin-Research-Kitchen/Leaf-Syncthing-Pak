package syncthing

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReadGameCheckStatusAggregatesLocalAndRemotePendingWork(t *testing.T) {
	peer := "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"
	transport := roundTripMap(t, map[string]string{
		"GET /rest/config/devices":                                       `[{"deviceID":"` + peer + `"}]`,
		"GET /rest/system/connections":                                   `{"connections":{"` + peer + `":{"connected":true}}}`,
		"GET /rest/db/status?folder=retro-saves":                         `{"state":"syncing","needBytes":40,"needTotalItems":2}`,
		"GET /rest/db/completion?device=" + peer + "&folder=retro-saves": `{"needBytes":20,"needItems":1,"needDeletes":1,"remoteState":"valid"}`,
	})
	process := &Process{client: &http.Client{Transport: transport}, apiKey: "secret"}
	status, err := process.ReadGameCheckStatus(context.Background(), []ConfiguredFolder{{
		ID: "retro-saves", Devices: []string{"SELF", peer},
	}}, "SELF")
	if err != nil {
		t.Fatal(err)
	}
	if status.Current || status.PendingItems != 4 || status.PendingBytes != 60 {
		t.Fatalf("game check = %+v", status)
	}
}

func TestReadGameCheckStatusReadsIndependentEndpointsConcurrently(t *testing.T) {
	peer := "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"
	responses := map[string]string{
		"GET /rest/config/devices":                                       `[{"deviceID":"` + peer + `"}]`,
		"GET /rest/system/connections":                                   `{"connections":{"` + peer + `":{"connected":true}}}`,
		"GET /rest/db/status?folder=retro-saves":                         `{"state":"idle"}`,
		"GET /rest/db/completion?device=" + peer + "&folder=retro-saves": `{"remoteState":"valid"}`,
	}
	configRelease := make(chan struct{})
	databaseRelease := make(chan struct{})
	configStarted := 0
	databaseStarted := 0
	var mutex sync.Mutex
	transport := uiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		key := request.Method + " " + request.URL.RequestURI()
		payload, found := responses[key]
		if !found {
			return nil, fmt.Errorf("unexpected upstream request %s", key)
		}
		mutex.Lock()
		release := databaseRelease
		if strings.HasPrefix(request.URL.Path, "/rest/config/") || request.URL.Path == "/rest/system/connections" {
			configStarted++
			release = configRelease
			if configStarted == 2 {
				close(configRelease)
			}
		} else {
			databaseStarted++
			if databaseStarted == 2 {
				close(databaseRelease)
			}
		}
		mutex.Unlock()
		select {
		case <-release:
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(payload)), Request: request}, nil
	})
	process := &Process{client: &http.Client{Transport: transport}, apiKey: "secret"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	status, err := process.ReadGameCheckStatus(ctx, []ConfiguredFolder{{
		ID: "retro-saves", Devices: []string{"SELF", peer},
	}}, "SELF")
	if err != nil || !status.Current {
		t.Fatalf("game check = %+v, %v", status, err)
	}
}

func TestReadGameCheckStatusWaitsForDisconnectedConfiguredPeer(t *testing.T) {
	peer := "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"
	transport := roundTripMap(t, map[string]string{
		"GET /rest/config/devices":               `[{"deviceID":"` + peer + `"}]`,
		"GET /rest/system/connections":           `{ "connections": {} }`,
		"GET /rest/db/status?folder=retro-saves": `{"state":"idle","needBytes":0,"needTotalItems":0}`,
	})
	process := &Process{client: &http.Client{Transport: transport}, apiKey: "secret"}
	status, err := process.ReadGameCheckStatus(context.Background(), []ConfiguredFolder{{
		ID: "retro-saves", Devices: []string{"SELF", peer},
	}}, "SELF")
	if err != nil || status.Current || status.PendingItems != 1 || status.PendingBytes != 0 {
		t.Fatalf("game check = %+v, %v", status, err)
	}
}

func TestReadGameCheckStatusRejectsUnconfiguredFolderPeer(t *testing.T) {
	peer := "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"
	transport := roundTripMap(t, map[string]string{
		"GET /rest/config/devices":     `[]`,
		"GET /rest/system/connections": `{ "connections": {} }`,
	})
	process := &Process{client: &http.Client{Transport: transport}, apiKey: "secret"}
	if _, err := process.ReadGameCheckStatus(context.Background(), []ConfiguredFolder{{
		ID: "retro-saves", Devices: []string{"SELF", peer},
	}}, "SELF"); err == nil {
		t.Fatal("unconfigured folder peer was accepted")
	}
}

func TestReadGameCheckStatusAcceptsCurrentLocalOnlyFolder(t *testing.T) {
	transport := roundTripMap(t, map[string]string{
		"GET /rest/config/devices":               `[]`,
		"GET /rest/system/connections":           `{ "connections": {} }`,
		"GET /rest/db/status?folder=retro-saves": `{"state":"idle","needBytes":0,"needTotalItems":0}`,
	})
	process := &Process{client: &http.Client{Transport: transport}, apiKey: "secret"}
	status, err := process.ReadGameCheckStatus(context.Background(), []ConfiguredFolder{{
		ID: "retro-saves", Devices: []string{"SELF"},
	}}, "SELF")
	if err != nil || !status.Current {
		t.Fatalf("game check = %+v, %v", status, err)
	}
}

func TestReadGameCheckStatusWaitsForUnknownRemoteState(t *testing.T) {
	peer := "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"
	transport := roundTripMap(t, map[string]string{
		"GET /rest/config/devices":                                       `[{"deviceID":"` + peer + `"}]`,
		"GET /rest/system/connections":                                   `{"connections":{"` + peer + `":{"connected":true}}}`,
		"GET /rest/db/status?folder=retro-saves":                         `{"state":"idle"}`,
		"GET /rest/db/completion?device=" + peer + "&folder=retro-saves": `{"remoteState":"unknown"}`,
	})
	process := &Process{client: &http.Client{Transport: transport}, apiKey: "secret"}
	status, err := process.ReadGameCheckStatus(context.Background(), []ConfiguredFolder{{
		ID: "retro-saves", Devices: []string{"SELF", peer},
	}}, "SELF")
	if err != nil || status.Current || status.PendingItems != 1 || status.PendingBytes != 0 {
		t.Fatalf("game check = %+v, %v", status, err)
	}
}

func TestReadGameCheckStatusRejectsNonSharingRemoteStates(t *testing.T) {
	peer := "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"
	for _, remoteState := range []string{"paused", "notSharing", "unexpected"} {
		t.Run(remoteState, func(t *testing.T) {
			transport := roundTripMap(t, map[string]string{
				"GET /rest/config/devices":                                       `[{"deviceID":"` + peer + `"}]`,
				"GET /rest/system/connections":                                   `{"connections":{"` + peer + `":{"connected":true}}}`,
				"GET /rest/db/status?folder=retro-saves":                         `{"state":"idle"}`,
				"GET /rest/db/completion?device=" + peer + "&folder=retro-saves": `{"remoteState":"` + remoteState + `"}`,
			})
			process := &Process{client: &http.Client{Transport: transport}, apiKey: "secret"}
			if _, err := process.ReadGameCheckStatus(context.Background(), []ConfiguredFolder{{
				ID: "retro-saves", Devices: []string{"SELF", peer},
			}}, "SELF"); err == nil {
				t.Fatalf("remote state %q was accepted", remoteState)
			}
		})
	}
}

func TestReadGameCheckStatusRejectsPausedConfiguredPeer(t *testing.T) {
	peer := "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"
	transport := roundTripMap(t, map[string]string{
		"GET /rest/config/devices":               `[{"deviceID":"` + peer + `","paused":true}]`,
		"GET /rest/system/connections":           `{"connections":{"` + peer + `":{"connected":true}}}`,
		"GET /rest/db/status?folder=retro-saves": `{"state":"idle","needBytes":0,"needTotalItems":0}`,
	})
	process := &Process{client: &http.Client{Transport: transport}, apiKey: "secret"}
	if _, err := process.ReadGameCheckStatus(context.Background(), []ConfiguredFolder{{
		ID: "retro-saves", Devices: []string{"SELF", peer},
	}}, "SELF"); err == nil {
		t.Fatal("paused configured peer was accepted as current")
	}
}
