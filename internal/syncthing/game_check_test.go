package syncthing

import (
	"context"
	"net/http"
	"testing"
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

func TestReadGameCheckStatusRequiresEverySelectedPeerCurrent(t *testing.T) {
	peer := "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"
	transport := roundTripMap(t, map[string]string{
		"GET /rest/config/devices":               `[{"deviceID":"` + peer + `"}]`,
		"GET /rest/system/connections":           `{ "connections": {} }`,
		"GET /rest/db/status?folder=retro-saves": `{"state":"idle","needBytes":0,"needTotalItems":0}`,
	})
	process := &Process{client: &http.Client{Transport: transport}, apiKey: "secret"}
	if _, err := process.ReadGameCheckStatus(context.Background(), []ConfiguredFolder{{
		ID: "retro-saves", Devices: []string{"SELF", peer},
	}}, "SELF"); err == nil {
		t.Fatal("offline selected peer was accepted as current")
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
