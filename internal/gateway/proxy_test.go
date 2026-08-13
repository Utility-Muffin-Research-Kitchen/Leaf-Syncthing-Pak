package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestProxyRequestsAnUncompressedRootForBannerInjection(t *testing.T) {
	manager := &Manager{options: Options{Upstream: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if encoding := request.Header.Get("Accept-Encoding"); encoding != "" {
			t.Fatalf("upstream Accept-Encoding = %q", encoding)
		}
		payload := `<!doctype html><html><body><main>Syncthing</main></body></html>`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader(payload)),
			Request:    request,
		}, nil
	})}}
	request := httptest.NewRequest(http.MethodGet, "https://leaf.example/", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()
	manager.newProxy().ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		strings.Count(response.Body.String(), `id="leaf-read-only-banner"`) != 1 {
		t.Fatalf("decorated proxy root = %d %s", response.Code, response.Body.String())
	}
}

func TestProxyAllowListRejectsUnknownAndSmuggledTargets(t *testing.T) {
	allowed := []string{
		"/", "/assets/css/theme.css", "/rest/system/status",
		"/vendor/fork-awesome/fonts/forkawesome-webfont.woff2?v=1.2.0",
		"/rest/events?since=1", "/rest/db/status?folder=saves",
		"/rest/db/completion?device=AAAA&folder=saves",
	}
	for _, raw := range allowed {
		target, _ := url.Parse(raw)
		if !allowProxyURL(target) {
			t.Errorf("expected allowed: %s", raw)
		}
	}
	rejected := []string{
		"/unknown", "/assets/css/unknown.css", "/rest/system/shutdown",
		"/assets/css/theme.css?v=1.2.0", "/vendor/fork-awesome/fonts/forkawesome-webfont.woff2?v=2",
		"/rest/events?since=one", "/rest/events?since=1&target=http://example.com",
		"/rest/db/status?folder=a&folder=b", "//example.com/rest/system/status",
		"/rest/%2e%2e/config",
	}
	for _, raw := range rejected {
		target, _ := url.Parse(raw)
		if allowProxyURL(target) {
			t.Errorf("expected rejected: %s", raw)
		}
	}
}

func TestNormalizeResponseAddsReadOnlyUIOverrides(t *testing.T) {
	request := &http.Request{URL: &url.URL{Path: "/assets/css/overrides.css"}}
	response := &http.Response{StatusCode: http.StatusOK, Request: request,
		Header: http.Header{"Content-Encoding": []string{"gzip"}},
		Body:   io.NopCloser(strings.NewReader(".upstream { color: blue; }")),
	}
	if err := normalizeUpstreamResponse(response); err != nil {
		t.Fatal(err)
	}
	payload, _ := io.ReadAll(response.Body)
	if !strings.Contains(string(payload), ".upstream") ||
		!strings.Contains(string(payload), `[ng-click^="addFolder("]`) ||
		response.Header.Get("Content-Encoding") != "" || response.ContentLength != int64(len(payload)) {
		t.Fatalf("read-only overrides missing: %q, %v", payload, response.Header)
	}
}

func TestNormalizeResponseStripsHeadersRedirectsAndSecrets(t *testing.T) {
	request := &http.Request{URL: &url.URL{Path: "/rest/config"}}
	response := &http.Response{StatusCode: http.StatusOK, Request: request,
		Header: http.Header{"Set-Cookie": []string{"bad=1"}, "Access-Control-Allow-Origin": []string{"*"}},
		Body:   io.NopCloser(strings.NewReader(`{"gui":{"apiKey":"secret","password":"hash"}}`)),
	}
	if err := normalizeUpstreamResponse(response); err != nil {
		t.Fatal(err)
	}
	payload, _ := io.ReadAll(response.Body)
	if strings.Contains(string(payload), "secret") || strings.Contains(string(payload), "hash") ||
		response.Header.Get("Set-Cookie") != "" || response.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("normalization failed: %s, %v", payload, response.Header)
	}
	redirect := &http.Response{StatusCode: http.StatusFound, Request: request,
		Header: http.Header{"Location": []string{"https://example.com/"}}, Body: http.NoBody}
	if err := normalizeUpstreamResponse(redirect); err == nil {
		t.Fatal("external redirect was accepted")
	}
}

func TestNormalizeRootAddsOneReadOnlyBannerAndUpdatesLength(t *testing.T) {
	request := &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/"}}
	response := &http.Response{StatusCode: http.StatusOK, Request: request,
		Header: http.Header{"Content-Type": []string{"text/html"}},
		Body: io.NopCloser(strings.NewReader(
			`<!doctype html><html><body class="theme-default"><main>Syncthing</main></body></html>`)),
	}
	if err := normalizeUpstreamResponse(response); err != nil {
		t.Fatal(err)
	}
	payload, _ := io.ReadAll(response.Body)
	if strings.Count(string(payload), `id="leaf-read-only-banner"`) != 1 ||
		!strings.Contains(string(payload), "Read-only Leaf status view. Make changes on the handheld.") ||
		response.ContentLength != int64(len(payload)) ||
		response.Header.Get("Content-Length") != strconv.Itoa(len(payload)) {
		t.Fatalf("decorated root = length:%d header:%s body:%s",
			response.ContentLength, response.Header.Get("Content-Length"), payload)
	}
	response.Body = io.NopCloser(strings.NewReader(string(payload)))
	if err := normalizeUpstreamResponse(response); err != nil {
		t.Fatal(err)
	}
	payload, _ = io.ReadAll(response.Body)
	if strings.Count(string(payload), `id="leaf-read-only-banner"`) != 1 {
		t.Fatalf("duplicate banner: %s", payload)
	}
}

func TestNormalizeHeadLeavesRootBodyUnchanged(t *testing.T) {
	const shell = `<!doctype html><html><body>Syncthing</body></html>`
	response := &http.Response{StatusCode: http.StatusOK,
		Request: &http.Request{Method: http.MethodHead, URL: &url.URL{Path: "/"}},
		Header:  http.Header{"Content-Type": []string{"text/html"}},
		Body:    io.NopCloser(strings.NewReader(shell)),
	}
	if err := normalizeUpstreamResponse(response); err != nil {
		t.Fatal(err)
	}
	payload, _ := io.ReadAll(response.Body)
	if string(payload) != shell {
		t.Fatalf("HEAD body changed: %s", payload)
	}
}
