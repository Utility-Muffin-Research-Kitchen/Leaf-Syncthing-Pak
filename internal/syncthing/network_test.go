package syncthing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync"
	"testing"
)

type captureTransport struct {
	header http.Header
}

func (transport *captureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.header = request.Header.Clone()
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
}

func TestGatewayTransportStripsClientCredentialsAndInjectsPrivateKey(t *testing.T) {
	capture := &captureTransport{}
	process := &Process{apiKey: "private", client: &http.Client{Transport: capture}}
	request, _ := http.NewRequest(http.MethodGet, "http://syncthing-unix/", nil)
	request.Header.Set("Authorization", "client")
	request.Header.Set("X-API-Key", "wrong")
	response, err := process.GatewayTransport().RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if capture.header.Get("Authorization") != "" || capture.header.Get("X-API-Key") != "private" {
		t.Fatalf("gateway headers = %v", capture.header)
	}
}

type rewriteTransport struct {
	base *url.URL
	next http.RoundTripper
}

func (transport rewriteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.URL.Scheme = transport.base.Scheme
	clone.URL.Host = transport.base.Host
	return transport.next.RoundTrip(clone)
}

func TestApplyLANOnlyPausesBeforePolicyAndRestores(t *testing.T) {
	var mu sync.Mutex
	steps := []string{}
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-API-Key") != "private-key" {
			t.Error("missing private API key")
		}
		mu.Lock()
		defer mu.Unlock()
		body, _ := io.ReadAll(request.Body)
		switch request.Method + " " + request.URL.Path {
		case "GET /rest/config/devices":
			steps = append(steps, "devices")
			_, _ = response.Write([]byte(`[{"deviceID":"SELF","paused":false},{"deviceID":"PEER","paused":false}]`))
		case "GET /rest/system/connections":
			steps = append(steps, "disconnected")
			_, _ = response.Write([]byte(`{"connections":{"PEER":{"connected":false,"address":""}}}`))
		case "PATCH /rest/config/devices/PEER":
			var patch map[string]any
			if err := json.Unmarshal(body, &patch); err != nil {
				t.Error(err)
			}
			switch {
			case patch["paused"] == true:
				steps = append(steps, "pause")
			case patch["paused"] == false:
				steps = append(steps, "unpause")
			case reflect.DeepEqual(patch["allowedNetworks"], []any{"192.168.4.0/24", "2001:db8::/64"}):
				steps = append(steps, "allow")
			default:
				t.Errorf("unexpected peer patch: %s", body)
			}
		case "PATCH /rest/config/options":
			var patch map[string]any
			if err := json.Unmarshal(body, &patch); err != nil {
				t.Error(err)
			}
			if patch["globalAnnounceEnabled"] != false || patch["localAnnounceEnabled"] != true ||
				patch["relaysEnabled"] != false || patch["natEnabled"] != false || patch["urAccepted"] != float64(-1) {
				t.Errorf("wrong LAN options: %v", patch)
			}
			steps = append(steps, "options")
		default:
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
			response.WriteHeader(http.StatusNotFound)
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	base, _ := url.Parse(server.URL)
	process := &Process{apiKey: "private-key", client: &http.Client{
		Transport: rewriteTransport{base: base, next: http.DefaultTransport},
	}}
	request := NetworkProfileRequest{
		Profile: NetworkLANOnly, SelfDeviceID: "SELF",
		AllowedNetworks: []string{"192.168.4.0/24", "2001:db8::/64"},
	}
	if err := process.ApplyNetworkProfile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	want := []string{"devices", "pause", "disconnected", "allow", "options", "unpause"}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("steps = %v, want %v", steps, want)
	}
}

func TestApplySyncAnywhereClearsBoundaryAsOneProfile(t *testing.T) {
	steps := []string{}
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		switch request.Method + " " + request.URL.Path {
		case "GET /rest/config/devices":
			steps = append(steps, "devices")
			_, _ = response.Write([]byte(`[{"deviceID":"SELF"},{"deviceID":"PEER","allowedNetworks":["192.168.4.0/24"]}]`))
		case "PATCH /rest/config/devices/PEER":
			if string(body) != `{"allowedNetworks":[]}` {
				t.Errorf("unexpected clear patch %s", body)
			}
			steps = append(steps, "clear")
		case "PATCH /rest/config/options":
			var patch map[string]any
			_ = json.Unmarshal(body, &patch)
			if patch["globalAnnounceEnabled"] != true || patch["relaysEnabled"] != true || patch["natEnabled"] != true {
				t.Errorf("wrong anywhere options: %v", patch)
			}
			steps = append(steps, "options")
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	base, _ := url.Parse(server.URL)
	process := &Process{apiKey: "private-key", client: &http.Client{
		Transport: rewriteTransport{base: base, next: http.DefaultTransport},
	}}
	if err := process.ApplyNetworkProfile(context.Background(), NetworkProfileRequest{
		Profile: NetworkSyncAnywhere, SelfDeviceID: "SELF",
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(steps, []string{"devices", "clear", "options"}) {
		t.Fatalf("steps = %v", steps)
	}
}
