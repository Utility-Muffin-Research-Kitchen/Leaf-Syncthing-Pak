package gateway

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func testManager(t *testing.T, now *time.Time) *Manager {
	t.Helper()
	upstream := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := `ok`
		contentType := "text/plain"
		if request.URL.Path == "/rest/config" {
			body = `{"gui":{"apiKey":"upstream-secret","password":"password-secret"},"devices":[]}`
			contentType = "application/json"
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{
			"Content-Type": []string{contentType}, "Set-Cookie": []string{"upstream=bad"},
			"Access-Control-Allow-Origin": []string{"*"},
		}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})
	manager, err := New(Options{
		StateDirectory: t.TempDir(), Upstream: upstream, Port: 0, AllowLoopback: true,
		Addresses: func() ([]net.IP, error) { return []net.IP{net.ParseIP("127.0.0.1")}, nil },
		Now:       func() time.Time { return *now },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	return manager
}

func insecureClient() *http.Client {
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} // test listener only
}

func bootstrapCSRF(t *testing.T, client *http.Client, base string) string {
	t.Helper()
	response, err := client.Get(base + "leaf/pair")
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	match := regexp.MustCompile(`const csrf="([^"]+)"`).FindSubmatch(payload)
	if response.StatusCode != http.StatusOK || len(match) != 2 {
		t.Fatalf("bootstrap = %d %s", response.StatusCode, payload)
	}
	return string(match[1])
}

func submitPair(t *testing.T, client *http.Client, status Status, csrf string, body any) *http.Response {
	t.Helper()
	payload, _ := json.Marshal(body)
	request, _ := http.NewRequest(http.MethodPost, status.URL+"leaf/pair", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", strings.TrimSuffix(status.URL, "/"))
	request.Header.Set("X-Leaf-CSRF", csrf)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestTickDoesNotInspectAddressesWhileClosed(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	addressCalls := 0
	manager, err := New(Options{
		StateDirectory: t.TempDir(), Upstream: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("unexpected upstream request")
		}),
		Port: 0, AllowLoopback: true, Now: func() time.Time { return now },
		Addresses: func() ([]net.IP, error) {
			addressCalls++
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if closed, err := manager.Tick(); err != nil || closed || addressCalls != 0 {
		t.Fatalf("closed tick = closed:%v err:%v address calls:%d", closed, err, addressCalls)
	}
}

func TestPairingProxyLogoutAndReadOnlyBoundary(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	manager := testManager(t, &now)
	status, err := manager.Open()
	if err != nil {
		t.Fatal(err)
	}
	client := insecureClient()
	csrf := bootstrapCSRF(t, client, status.URL)
	pairResponse := submitPair(t, client, status, csrf, map[string]string{"pin": status.PIN})
	if pairResponse.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(pairResponse.Body)
		t.Fatalf("pair = %d %s", pairResponse.StatusCode, body)
	}
	var trustCookie *http.Cookie
	for _, cookie := range pairResponse.Cookies() {
		if cookie.Name == trustCookieName {
			trustCookie = cookie
		}
	}
	_ = pairResponse.Body.Close()
	if trustCookie == nil || trustCookie.Domain != "" || trustCookie.Path != "/" ||
		!trustCookie.HttpOnly || !trustCookie.Secure || trustCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("trust cookie = %+v", trustCookie)
	}

	replay := submitPair(t, client, status, csrf, map[string]string{"pin": status.PIN})
	if replay.StatusCode != http.StatusUnauthorized {
		t.Fatalf("pair replay status = %d", replay.StatusCode)
	}
	_ = replay.Body.Close()

	get := func(path string) *http.Response {
		request, _ := http.NewRequest(http.MethodGet, status.URL+strings.TrimPrefix(path, "/"), nil)
		request.AddCookie(trustCookie)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	config := get("/rest/config")
	configPayload, _ := io.ReadAll(config.Body)
	_ = config.Body.Close()
	if config.StatusCode != http.StatusOK || bytes.Contains(configPayload, []byte("upstream-secret")) ||
		bytes.Contains(configPayload, []byte("password-secret")) || config.Header.Get("Set-Cookie") != "" ||
		config.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("normalized config = %d headers=%v body=%s", config.StatusCode, config.Header, configPayload)
	}
	unknown := get("/rest/system/shutdown")
	if unknown.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown path status = %d", unknown.StatusCode)
	}
	_ = unknown.Body.Close()
	postRequest, _ := http.NewRequest(http.MethodPost, status.URL+"rest/config", nil)
	postRequest.AddCookie(trustCookie)
	post, err := client.Do(postRequest)
	if err != nil {
		t.Fatal(err)
	}
	if post.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("upstream POST status = %d", post.StatusCode)
	}
	_ = post.Body.Close()

	logoutPayload := bytes.NewReader([]byte(`{}`))
	logoutRequest, _ := http.NewRequest(http.MethodPost, status.URL+"leaf/logout", logoutPayload)
	logoutRequest.AddCookie(trustCookie)
	logoutRequest.Header.Set("Content-Type", "application/json")
	logoutRequest.Header.Set("Origin", strings.TrimSuffix(status.URL, "/"))
	logoutRequest.Header.Set("X-Leaf-CSRF", csrf)
	logout, err := client.Do(logoutRequest)
	if err != nil || logout.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %v, %v", logout, err)
	}
	_ = logout.Body.Close()
	after := get("/")
	if after.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked cookie status = %d", after.StatusCode)
	}
	_ = after.Body.Close()
}

func TestQROfferFragmentSingleUseAndSecondOfferInvalidatesFirst(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	manager := testManager(t, &now)
	first, err := manager.Open()
	if err != nil {
		t.Fatal(err)
	}
	firstURL, _ := url.Parse(first.QRURL)
	firstToken := firstURL.Fragment
	if firstURL.RawQuery != "" || !strings.HasPrefix(firstToken, "token=") {
		t.Fatalf("QR token is not fragment-only: %s", first.QRURL)
	}
	firstToken = strings.TrimPrefix(firstToken, "token=")
	second, err := manager.Open()
	if err != nil {
		t.Fatal(err)
	}
	client := insecureClient()
	csrf := bootstrapCSRF(t, client, second.URL)
	old := submitPair(t, client, second, csrf, map[string]string{"token": firstToken})
	if old.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalidated QR status = %d", old.StatusCode)
	}
	_ = old.Body.Close()
	secondURL, _ := url.Parse(second.QRURL)
	token, _ := url.QueryUnescape(strings.TrimPrefix(secondURL.Fragment, "token="))
	accepted := submitPair(t, client, second, csrf, map[string]string{"token": token})
	if accepted.StatusCode != http.StatusNoContent {
		t.Fatalf("current QR status = %d", accepted.StatusCode)
	}
	_ = accepted.Body.Close()
	replay := submitPair(t, client, second, csrf, map[string]string{"token": token})
	if replay.StatusCode != http.StatusUnauthorized {
		t.Fatalf("QR replay status = %d", replay.StatusCode)
	}
	_ = replay.Body.Close()
}

func TestAdversarialBodiesAndMethodOverridesFailClosed(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	manager := testManager(t, &now)
	status, err := manager.Open()
	if err != nil {
		t.Fatal(err)
	}
	client := insecureClient()
	csrf := bootstrapCSRF(t, client, status.URL)
	paired := submitPair(t, client, status, csrf, map[string]string{"pin": status.PIN})
	var trustCookie *http.Cookie
	for _, cookie := range paired.Cookies() {
		if cookie.Name == trustCookieName {
			trustCookie = cookie
		}
	}
	_ = paired.Body.Close()
	if trustCookie == nil {
		t.Fatal("pairing did not issue trust")
	}

	for _, test := range []struct {
		name   string
		method string
		body   io.Reader
		header http.Header
		want   int
	}{
		{name: "get-body", method: http.MethodGet, body: strings.NewReader("mutation"), want: http.StatusBadRequest},
		{name: "method-override", method: http.MethodGet, header: http.Header{"X-HTTP-Method-Override": []string{"DELETE"}}, want: http.StatusBadRequest},
		{name: "connect", method: http.MethodConnect, want: http.StatusMethodNotAllowed},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequest(test.method, status.URL+"rest/config", test.body)
			if err != nil {
				t.Fatal(err)
			}
			request.Header = test.header
			if request.Header == nil {
				request.Header = make(http.Header)
			}
			request.AddCookie(trustCookie)
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != test.want {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.want)
			}
		})
	}

	oversized := bytes.NewReader(bytes.Repeat([]byte("x"), 4097))
	request, _ := http.NewRequest(http.MethodPost, status.URL+"leaf/pair", oversized)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", strings.TrimSuffix(status.URL, "/"))
	request.Header.Set("X-Leaf-CSRF", csrf)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized pairing status = %d", response.StatusCode)
	}
}

func TestPairingExpiryRateLimitsAndExtensionLifetime(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	manager := testManager(t, &now)
	status, err := manager.Open()
	if err != nil {
		t.Fatal(err)
	}
	qrURL, _ := url.Parse(status.QRURL)
	qrToken, _ := url.QueryUnescape(strings.TrimPrefix(qrURL.Fragment, "token="))
	now = now.Add(pairingLifetime + time.Second)
	request := directPairRequest(status, manager.controlCSRF, "192.0.2.1:1000", "0000")
	recorder := httptest.NewRecorder()
	manager.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expired PIN status = %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	manager.ServeHTTP(recorder, directTokenRequest(status, manager.controlCSRF, "192.0.2.3:1000", qrToken))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expired QR token status = %d", recorder.Code)
	}

	now = now.Add(time.Second)
	status, _ = manager.Open()
	for attempt := 0; attempt < 6; attempt++ {
		recorder = httptest.NewRecorder()
		manager.ServeHTTP(recorder, directPairRequest(status, manager.controlCSRF, "192.0.2.2:1000", "xxxx"))
		want := http.StatusUnauthorized
		if attempt == 5 {
			want = http.StatusTooManyRequests
		}
		if recorder.Code != want {
			t.Fatalf("source attempt %d = %d, want %d", attempt+1, recorder.Code, want)
		}
	}
	status, _ = manager.Open()
	for attempt := 0; attempt < 20; attempt++ {
		recorder = httptest.NewRecorder()
		source := net.JoinHostPort("192.0.2."+strconv.Itoa(attempt+10), "1000")
		manager.ServeHTTP(recorder, directPairRequest(status, manager.controlCSRF, source, "xxxx"))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("global attempt %d = %d", attempt+1, recorder.Code)
		}
	}
	recorder = httptest.NewRecorder()
	manager.ServeHTTP(recorder, directPairRequest(status, manager.controlCSRF, "198.51.100.1:1000", status.PIN))
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("global lock status = %d", recorder.Code)
	}
	status, _ = manager.Open()
	client := insecureClient()
	csrf := bootstrapCSRF(t, client, status.URL)
	paired := submitPair(t, client, status, csrf, map[string]string{"pin": status.PIN})
	_ = paired.Body.Close()
	extended, err := manager.Extend()
	if err != nil || extended.Pairing || extended.ExtensionExpires.Sub(now) != extensionLifetime {
		t.Fatalf("extension = %+v, %v", extended, err)
	}
	now = now.Add(extensionLifetime + time.Second)
	closed, _ := manager.Tick()
	if !closed || manager.Status().Open {
		t.Fatal("gateway remained open after the fixed extension")
	}
}

func directPairRequest(status Status, csrf, remote, pin string) *http.Request {
	payload, _ := json.Marshal(map[string]string{"pin": pin})
	return directPairingRequest(status, csrf, remote, payload)
}

func directTokenRequest(status Status, csrf, remote, token string) *http.Request {
	payload, _ := json.Marshal(map[string]string{"token": token})
	return directPairingRequest(status, csrf, remote, payload)
}

func directPairingRequest(status Status, csrf, remote string, payload []byte) *http.Request {
	request := httptest.NewRequest(http.MethodPost, status.URL+"leaf/pair", bytes.NewReader(payload))
	request.Host = strings.TrimPrefix(strings.TrimSuffix(status.URL, "/"), "https://")
	request.RemoteAddr = remote
	request.Header.Set("Origin", strings.TrimSuffix(status.URL, "/"))
	request.Header.Set("X-Leaf-CSRF", csrf)
	request.Header.Set("Content-Type", "application/json")
	return request
}
