package gateway

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestProxyAllowListRejectsUnknownAndSmuggledTargets(t *testing.T) {
	allowed := []string{
		"/", "/assets/css/theme.css", "/rest/system/status",
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
