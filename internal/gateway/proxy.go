package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
)

const (
	maxProxyJSON   = 4 * 1024 * 1024
	maxProxyHTML   = 4 * 1024 * 1024
	readOnlyBanner = `<div id="leaf-read-only-banner" role="status" aria-live="polite" style="position:sticky;top:0;z-index:2147483647;padding:10px 16px;background:#f6c344;color:#161616;font:600 16px sans-serif;text-align:center">Read-only Leaf status view. Make changes on the handheld.</div>`
)

func (manager *Manager) newProxy() http.Handler {
	upstream, _ := url.Parse("http://syncthing-unix")
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.Transport = manager.options.Upstream
	proxy.FlushInterval = -1
	baseDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		baseDirector(request)
		request.Host = "syncthing-unix"
		request.Header.Del("Authorization")
		request.Header.Del("X-API-Key")
		request.Header.Del("Cookie")
		request.Header.Del("Accept-Encoding")
		request.Header.Del("Origin")
		request.Header.Del("Referer")
	}
	proxy.ModifyResponse = normalizeUpstreamResponse
	proxy.ErrorHandler = func(response http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(response, "Syncthing status is temporarily unavailable.", http.StatusBadGateway)
	}
	return proxy
}

func normalizeUpstreamResponse(response *http.Response) error {
	response.Header.Del("Set-Cookie")
	response.Header.Set("Cache-Control", "no-store")
	for name := range response.Header {
		if strings.HasPrefix(strings.ToLower(name), "access-control-") {
			response.Header.Del(name)
		}
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		location := response.Header.Get("Location")
		if location != "" {
			parsed, err := url.Parse(location)
			if err != nil || parsed.IsAbs() || parsed.Host != "" || !allowProxyURL(parsed) {
				return errors.New("upstream external or unknown redirect rejected")
			}
		}
	}
	if response.StatusCode == http.StatusOK && response.Request != nil &&
		response.Request.Method == http.MethodGet && response.Request.URL.Path == "/" &&
		response.Body != nil {
		if err := addReadOnlyBanner(response); err != nil {
			return err
		}
	}
	if response.Request != nil && response.Request.URL.Path == "/rest/config" && response.Body != nil {
		payload, err := io.ReadAll(io.LimitReader(response.Body, maxProxyJSON+1))
		_ = response.Body.Close()
		if err != nil || len(payload) > maxProxyJSON {
			return errors.New("upstream config response exceeds gateway limit")
		}
		var configuration any
		decoder := json.NewDecoder(bytes.NewReader(payload))
		if err := decoder.Decode(&configuration); err != nil {
			return errors.New("upstream config response is invalid")
		}
		redactSecrets(configuration)
		payload, err = json.Marshal(configuration)
		if err != nil {
			return err
		}
		response.Body = io.NopCloser(bytes.NewReader(payload))
		response.ContentLength = int64(len(payload))
		response.Header.Set("Content-Length", strconv.Itoa(len(payload)))
		response.Header.Del("Content-Encoding")
	}
	return nil
}

func addReadOnlyBanner(response *http.Response) error {
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxProxyHTML+1))
	_ = response.Body.Close()
	if err != nil || len(payload) > maxProxyHTML {
		return errors.New("upstream UI shell exceeds gateway limit")
	}
	if !bytes.Contains(payload, []byte(`id="leaf-read-only-banner"`)) {
		lower := bytes.ToLower(payload)
		body := bytes.Index(lower, []byte("<body"))
		if body < 0 {
			return errors.New("upstream UI shell has no body")
		}
		bodyEnd := bytes.IndexByte(lower[body:], '>')
		if bodyEnd < 0 {
			return errors.New("upstream UI shell has an invalid body")
		}
		bodyEnd += body + 1
		decorated := make([]byte, 0, len(payload)+len(readOnlyBanner))
		decorated = append(decorated, payload[:bodyEnd]...)
		decorated = append(decorated, readOnlyBanner...)
		payload = append(decorated, payload[bodyEnd:]...)
	}
	response.Body = io.NopCloser(bytes.NewReader(payload))
	response.ContentLength = int64(len(payload))
	response.Header.Set("Content-Length", strconv.Itoa(len(payload)))
	response.Header.Del("Content-Encoding")
	return nil
}

func redactSecrets(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for name, child := range typed {
			switch strings.ToLower(name) {
			case "apikey", "password", "untrustedpassword", "token":
				typed[name] = ""
			default:
				redactSecrets(child)
			}
		}
	case []any:
		for _, child := range typed {
			redactSecrets(child)
		}
	}
}

func allowProxyURL(target *url.URL) bool {
	if target == nil || target.IsAbs() || target.Host != "" || target.RawPath != "" || target.Fragment != "" || strings.Contains(target.Path, "//") ||
		strings.Contains(target.EscapedPath(), "%2e") || strings.Contains(target.EscapedPath(), "%2E") {
		return false
	}
	if _, allowed := staticPaths[target.Path]; allowed {
		return target.RawQuery == ""
	}
	query := target.Query()
	exactNoQuery := map[string]struct{}{
		"/rest/noauth/health": {}, "/rest/system/status": {}, "/rest/system/version": {},
		"/rest/system/connections": {}, "/rest/system/error": {}, "/rest/system/discovery": {},
		"/rest/config": {}, "/rest/config/insync": {}, "/rest/cluster/pending/devices": {},
		"/rest/cluster/pending/folders": {}, "/rest/stats/device": {}, "/rest/stats/folder": {},
		"/rest/svc/lang": {},
	}
	if _, allowed := exactNoQuery[target.Path]; allowed {
		return target.RawQuery == ""
	}
	switch target.Path {
	case "/rest/events":
		return numericQueryOnly(query, "since", "limit", "timeout")
	case "/rest/events/disk":
		return numericQueryOnly(query, "limit")
	case "/rest/db/status":
		return boundedQuery(query, "folder")
	case "/rest/db/completion":
		return boundedQueries(query, "device", "folder")
	}
	return false
}

func numericQueryOnly(query url.Values, names ...string) bool {
	allowed := make(map[string]struct{}, len(names))
	for _, name := range names {
		allowed[name] = struct{}{}
	}
	for name, values := range query {
		if _, ok := allowed[name]; !ok || len(values) != 1 || values[0] == "" {
			return false
		}
		if _, err := strconv.ParseUint(values[0], 10, 64); err != nil {
			return false
		}
	}
	return true
}

func boundedQuery(query url.Values, name string) bool {
	return boundedQueries(query, name)
}

func boundedQueries(query url.Values, names ...string) bool {
	if len(query) != len(names) {
		return false
	}
	for _, name := range names {
		values := query[name]
		if len(values) != 1 || len(values[0]) == 0 || len(values[0]) > 128 {
			return false
		}
	}
	return true
}

var staticPaths = func() map[string]struct{} {
	paths := []string{
		"/", "/meta.js", "/themes.json",
		"/assets/img/favicon-default.png", "/assets/img/safari-pinned-tab.svg", "/assets/img/logo-horizontal.svg",
		"/assets/font/raleway.css", "/assets/font/raleway-500.woff",
		"/assets/css/tree.css", "/assets/css/overrides.css", "/assets/css/theme.css",
		"/vendor/bootstrap/css/bootstrap.css", "/vendor/daterangepicker/daterangepicker.css",
		"/vendor/fork-awesome/css/fork-awesome.css", "/vendor/fork-awesome/css/v5-compat.css",
		"/vendor/fork-awesome/fonts/forkawesome-webfont.eot", "/vendor/fork-awesome/fonts/forkawesome-webfont.woff2",
		"/vendor/fork-awesome/fonts/forkawesome-webfont.woff", "/vendor/fork-awesome/fonts/forkawesome-webfont.ttf",
		"/vendor/fork-awesome/fonts/forkawesome-webfont.svg",
		"/vendor/bootstrap/fonts/reception-0.svg", "/vendor/bootstrap/fonts/reception-1.svg",
		"/vendor/bootstrap/fonts/reception-2.svg", "/vendor/bootstrap/fonts/reception-3.svg", "/vendor/bootstrap/fonts/reception-4.svg",
		"/vendor/jquery/jquery-3.7.1.js", "/vendor/angular/angular.js", "/vendor/angular/angular-sanitize.js",
		"/vendor/angular/angular-translate.js", "/vendor/angular/angular-translate-loader-static-files.js",
		"/vendor/angular/angular-dirPagination.js", "/vendor/moment/moment.js", "/vendor/bootstrap/js/bootstrap.js",
		"/vendor/daterangepicker/daterangepicker.js", "/vendor/fancytree/jquery.fancytree-all-deps.js",
		"/vendor/HumanizeDuration.js/humanize-duration.js", "/assets/lang/valid-langs.js", "/assets/lang/prettyprint.js",
		"/syncthing/app.js", "/syncthing/development/logbar.js", "/syncthing/development/logbar.html",
		"/syncthing/core/module.js", "/syncthing/core/alwaysNumberFilter.js", "/syncthing/core/basenameFilter.js",
		"/syncthing/core/binaryFilter.js", "/syncthing/core/localeNumberFilter.js", "/syncthing/core/percentFilter.js",
		"/syncthing/core/durationFilter.js", "/syncthing/core/eventService.js", "/syncthing/core/identiconDirective.js",
		"/syncthing/core/languageSelectDirective.js", "/syncthing/core/localeService.js", "/syncthing/core/modalDirective.js",
		"/syncthing/core/metricFilter.js", "/syncthing/core/notificationDirective.js", "/syncthing/core/pathIsSubDirDirective.js",
		"/syncthing/core/popoverDirective.js", "/syncthing/core/syncthingController.js", "/syncthing/core/tooltipDirective.js",
		"/syncthing/core/uncamelFilter.js", "/syncthing/core/uniqueFolderDirective.js", "/syncthing/core/validDeviceidDirective.js",
		"/syncthing/core/notifications.html", "/syncthing/core/networkErrorDialogView.html", "/syncthing/core/httpErrorDialogView.html",
		"/syncthing/core/restartingDialogView.html", "/syncthing/core/upgradingDialogView.html", "/syncthing/core/shutdownDialogView.html",
		"/syncthing/core/savingChangesDialogView.html", "/syncthing/core/upgradeModalView.html", "/syncthing/core/majorUpgradeModalView.html",
		"/syncthing/core/aboutModalView.html", "/syncthing/core/connectivityStatusModalView.html", "/syncthing/core/logViewerModalView.html",
		"/syncthing/device/idqrModalView.html", "/syncthing/device/editDeviceModalView.html", "/syncthing/device/globalChangesModalView.html",
		"/syncthing/device/removeDeviceDialogView.html", "/syncthing/device/shareDeviceIdDialogView.html",
		"/syncthing/folder/editFolderModalView.html", "/syncthing/folder/restoreVersionsModalView.html",
		"/syncthing/folder/restoreVersionsConfirmation.html", "/syncthing/folder/removeFolderDialogView.html",
		"/syncthing/folder/revertOverrideView.html", "/syncthing/settings/settingsModalView.html",
		"/syncthing/settings/advancedSettingsModalView.html", "/syncthing/settings/discardChangesConfirmation.html",
		"/syncthing/usagereport/usageReportModalView.html", "/syncthing/usagereport/usageReportPreviewModalView.html",
		"/syncthing/transfer/neededFilesModalView.html", "/syncthing/transfer/failedFilesModalView.html",
		"/syncthing/transfer/remoteNeededFilesModalView.html", "/syncthing/transfer/localChangedFilesModalView.html",
		"/theme-assets/light/assets/css/theme.css", "/theme-assets/dark/assets/css/theme.css",
		"/theme-assets/black/assets/css/theme.css", "/theme-assets/default/assets/css/theme.css",
	}
	for _, language := range []string{"ar", "bg", "ca", "ca@valencia", "cs", "da", "de", "el", "en", "en-GB", "eo", "es", "eu", "fil", "fr", "fy", "ga", "gl", "he-IL", "hi", "hr", "hu", "id", "it", "ja", "ko-KR", "lt", "nl", "pl", "pt-BR", "pt-PT", "ro-RO", "ru", "sk", "sl", "sv", "tr", "uk", "zh-CN", "zh-HK", "zh-TW"} {
		paths = append(paths, "/assets/lang/lang-"+language+".json")
	}
	allowed := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		allowed[path] = struct{}{}
	}
	return allowed
}()
