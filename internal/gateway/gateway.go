package gateway

import (
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	pairingLifetime    = 120 * time.Second
	extensionLifetime  = 15 * time.Minute
	foregroundLifetime = 4 * time.Second
	perSourceWindow    = 30 * time.Second
	globalWindow       = 10 * time.Minute
	trustCookieName    = "leaf_syncthing_trust"
)

type AddressSource func() ([]net.IP, error)

type Options struct {
	StateDirectory string
	Upstream       http.RoundTripper
	Addresses      AddressSource
	Port           int
	Now            func() time.Time
	Random         io.Reader
	AllowLoopback  bool
}

type Status struct {
	Open             bool
	URL              string
	PIN              string
	QRURL            string
	OfferExpires     time.Time
	Fingerprint      string
	TrustedBrowsers  int
	Pairing          bool
	ExtensionExpires time.Time
}

type pairingOffer struct {
	pin     string
	token   string
	expires time.Time
}

type instance struct {
	server      *http.Server
	listener    net.Listener
	done        chan struct{}
	host        string
	addresses   []string
	fingerprint string
}

type Manager struct {
	mu                 sync.Mutex
	options            Options
	trust              *trustStore
	proxy              http.Handler
	instance           *instance
	offer              *pairingOffer
	controlCSRF        string
	foregroundDeadline time.Time
	extensionExpires   time.Time
	failedBySource     map[string][]time.Time
	failedGlobal       []time.Time
	pairingLocked      bool
}

func New(options Options) (*Manager, error) {
	if !filepath.IsAbs(options.StateDirectory) || options.Upstream == nil || options.Addresses == nil ||
		options.Port < 0 || options.Port > 65535 {
		return nil, errors.New("gateway requires state, upstream transport, addresses, and a valid port")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	trust, err := newTrustStore(filepath.Join(options.StateDirectory, "trusted-clients.json"), options.Now, options.Random)
	if err != nil {
		return nil, err
	}
	manager := &Manager{
		options: options, trust: trust,
		failedBySource: make(map[string][]time.Time),
	}
	manager.proxy = manager.newProxy()
	return manager, nil
}

func (manager *Manager) Open() (Status, error) {
	addresses, err := manager.currentAddresses()
	if err != nil {
		return Status{}, err
	}
	addressStrings := ipStrings(addresses)
	manager.mu.Lock()
	current := manager.instance
	same := current != nil && slices.Equal(current.addresses, addressStrings)
	manager.mu.Unlock()
	if !same && current != nil {
		manager.Close()
	}
	if !same {
		certificate, err := ensureCertificate(manager.options.StateDirectory, addresses)
		if err != nil {
			return Status{}, err
		}
		listenIP := chooseListenIP(addresses)
		listener, err := net.Listen("tcp", net.JoinHostPort(listenIP.String(), strconv.Itoa(manager.options.Port)))
		if err != nil {
			return Status{}, fmt.Errorf("open gateway listener: %w", err)
		}
		host := listener.Addr().String()
		server := &http.Server{
			Handler: manager, ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10,
		}
		tlsListener := tlsListener(listener, certificate.Certificate)
		created := &instance{server: server, listener: listener, done: make(chan struct{}),
			host: host, addresses: addressStrings, fingerprint: certificate.Fingerprint}
		manager.mu.Lock()
		manager.instance = created
		manager.mu.Unlock()
		go func() {
			_ = server.Serve(tlsListener)
			close(created.done)
		}()
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.instance == nil {
		return Status{}, errors.New("gateway listener closed while opening")
	}
	manager.extensionExpires = time.Time{}
	manager.foregroundDeadline = manager.options.Now().Add(foregroundLifetime)
	manager.failedBySource = make(map[string][]time.Time)
	manager.failedGlobal = nil
	manager.pairingLocked = false
	if err := manager.issueOfferLocked(); err != nil {
		return Status{}, err
	}
	return manager.statusLocked(), nil
}

func (manager *Manager) KeepAlive() (Status, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.instance == nil {
		return Status{}, errors.New("gateway is closed")
	}
	if manager.extensionExpires.IsZero() {
		manager.foregroundDeadline = manager.options.Now().Add(foregroundLifetime)
	}
	return manager.statusLocked(), nil
}

func (manager *Manager) Extend() (Status, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.instance == nil || manager.trust.Count() == 0 {
		return Status{}, errors.New("a trusted browser is required before extending")
	}
	manager.offer = nil
	manager.extensionExpires = manager.options.Now().Add(extensionLifetime)
	manager.foregroundDeadline = time.Time{}
	return manager.statusLocked(), nil
}

func (manager *Manager) CloseForeground() error {
	manager.mu.Lock()
	extended := manager.instance != nil && !manager.extensionExpires.IsZero() &&
		manager.options.Now().Before(manager.extensionExpires)
	if extended {
		manager.offer = nil
	}
	manager.mu.Unlock()
	if extended {
		return nil
	}
	manager.Close()
	return nil
}

func (manager *Manager) RevokeAll() error {
	if err := manager.trust.RevokeAll(); err != nil {
		return err
	}
	manager.Close()
	return nil
}

func (manager *Manager) Close() {
	manager.mu.Lock()
	current := manager.instance
	manager.instance = nil
	manager.offer = nil
	manager.controlCSRF = ""
	manager.foregroundDeadline = time.Time{}
	manager.extensionExpires = time.Time{}
	manager.mu.Unlock()
	if current != nil {
		_ = current.server.Close()
		_ = current.listener.Close()
		select {
		case <-current.done:
		case <-time.After(2 * time.Second):
		}
	}
}

func (manager *Manager) Tick() (bool, error) {
	addresses, addressErr := manager.currentAddresses()
	manager.mu.Lock()
	current := manager.instance
	if current == nil {
		manager.mu.Unlock()
		return false, nil
	}
	now := manager.options.Now()
	closeForLifetime := (!manager.extensionExpires.IsZero() && !now.Before(manager.extensionExpires)) ||
		(manager.extensionExpires.IsZero() && !manager.foregroundDeadline.IsZero() && !now.Before(manager.foregroundDeadline))
	if manager.offer != nil && !now.Before(manager.offer.expires) {
		manager.offer = nil
	}
	addressChanged := addressErr != nil || !slices.Equal(current.addresses, ipStrings(addresses))
	manager.mu.Unlock()
	if closeForLifetime || addressChanged {
		manager.Close()
		return true, addressErr
	}
	return false, nil
}

func (manager *Manager) Status() Status {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.statusLocked()
}

func (manager *Manager) statusLocked() Status {
	status := Status{TrustedBrowsers: manager.trust.Count()}
	if manager.instance == nil {
		return status
	}
	status.Open = true
	status.URL = "https://" + manager.instance.host + "/"
	status.Fingerprint = manager.instance.fingerprint
	status.ExtensionExpires = manager.extensionExpires
	if manager.offer != nil && manager.extensionExpires.IsZero() {
		status.Pairing = true
		status.PIN = manager.offer.pin
		status.QRURL = "https://" + manager.instance.host + "/leaf/pair#token=" + url.QueryEscape(manager.offer.token)
		status.OfferExpires = manager.offer.expires
	}
	return status
}

func (manager *Manager) currentAddresses() ([]net.IP, error) {
	addresses, err := manager.options.Addresses()
	if err != nil {
		return nil, err
	}
	addresses = normalizedIPs(addresses)
	if len(addresses) == 0 {
		return nil, errors.New("no eligible LAN address")
	}
	if !manager.options.AllowLoopback {
		for _, address := range addresses {
			if address.IsLoopback() || address.IsUnspecified() {
				return nil, errors.New("gateway refuses a non-LAN address")
			}
		}
	}
	return addresses, nil
}

func (manager *Manager) issueOfferLocked() error {
	token, err := randomToken(manager.options.Random, 32)
	if err != nil {
		return err
	}
	csrf, err := randomToken(manager.options.Random, 32)
	if err != nil {
		return err
	}
	pinBytes := make([]byte, 2)
	if _, err := io.ReadFull(manager.options.Random, pinBytes); err != nil {
		return err
	}
	pin := int(binary.BigEndian.Uint16(pinBytes)) % 10000
	manager.offer = &pairingOffer{pin: fmt.Sprintf("%04d", pin), token: token,
		expires: manager.options.Now().Add(pairingLifetime)}
	manager.controlCSRF = csrf
	return nil
}

func randomToken(source io.Reader, bytes int) (string, error) {
	payload := make([]byte, bytes)
	if _, err := io.ReadFull(source, payload); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func ipStrings(addresses []net.IP) []string {
	values := make([]string, len(addresses))
	for index, address := range addresses {
		values[index] = address.String()
	}
	return values
}

func chooseListenIP(addresses []net.IP) net.IP {
	for _, address := range addresses {
		if address.To4() != nil {
			return address
		}
	}
	return addresses[0]
}

func (manager *Manager) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	secureHeaders(response.Header())
	if strings.HasPrefix(request.URL.Path, "/leaf/") {
		manager.serveControl(response, request)
		return
	}
	if !manager.authenticated(request) {
		http.Error(response, "Pair this browser from the Leaf Syncthing screen.", http.StatusUnauthorized)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		methodRejected(response)
		return
	}
	if request.ContentLength > 0 || len(request.TransferEncoding) != 0 || hasMethodOverride(request.Header) {
		http.Error(response, "Request bodies and method overrides are not accepted.", http.StatusBadRequest)
		return
	}
	if !allowProxyURL(request.URL) {
		http.Error(response, "This path is not part of the read-only Syncthing view.", http.StatusNotFound)
		return
	}
	manager.proxy.ServeHTTP(response, request)
}

func (manager *Manager) serveControl(response http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/leaf/pair":
		if request.Method == http.MethodGet || request.Method == http.MethodHead {
			manager.serveBootstrap(response, request)
			return
		}
		if request.Method == http.MethodPost {
			manager.submitPairing(response, request)
			return
		}
	case "/leaf/logout":
		if request.Method == http.MethodPost {
			manager.logout(response, request)
			return
		}
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		methodRejected(response)
		return
	}
	http.NotFound(response, request)
}

func (manager *Manager) serveBootstrap(response http.ResponseWriter, request *http.Request) {
	manager.mu.Lock()
	csrf := manager.controlCSRF
	pairing := manager.offer != nil && manager.extensionExpires.IsZero()
	manager.mu.Unlock()
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	if request.Method == http.MethodHead {
		return
	}
	state := "Pairing is open. Enter the four-digit PIN shown on the device."
	if !pairing {
		state = "Pairing is closed. Re-open the Web interface from the device."
	}
	page := `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Leaf Syncthing Pairing</title><h1>Leaf Syncthing</h1><p>` + html.EscapeString(state) + `</p><form id="pair"><input id="pin" inputmode="numeric" pattern="[0-9]{4}" maxlength="4" aria-label="PIN"><button>Pair</button></form><button id="logout" type="button">Log out this browser</button><p id="result"></p><script>const csrf=` + strconv.Quote(csrf) + `;const result=document.getElementById('result');async function send(path,body){const r=await fetch(path,{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json','X-Leaf-CSRF':csrf},body:JSON.stringify(body)});result.textContent=r.ok?'Done':await r.text();if(r.ok&&path==='/leaf/pair')location='/';}document.getElementById('pair').onsubmit=e=>{e.preventDefault();send('/leaf/pair',{pin:document.getElementById('pin').value});};document.getElementById('logout').onclick=()=>send('/leaf/logout',{});const token=new URLSearchParams(location.hash.slice(1)).get('token');if(token){history.replaceState(null,'',location.pathname);send('/leaf/pair',{token});}</script>`
	_, _ = io.WriteString(response, page)
}

func (manager *Manager) submitPairing(response http.ResponseWriter, request *http.Request) {
	if !manager.validControlRequest(request) {
		http.Error(response, "Pairing request origin was rejected.", http.StatusForbidden)
		return
	}
	var input struct {
		PIN   string `json:"pin"`
		Token string `json:"token"`
	}
	request.Body = http.MaxBytesReader(response, request.Body, 4096)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil ||
		decoder.Decode(&struct{}{}) != io.EOF || (input.PIN == "") == (input.Token == "") {
		http.Error(response, "Invalid pairing request.", http.StatusBadRequest)
		return
	}
	source, _, _ := net.SplitHostPort(request.RemoteAddr)
	now := manager.options.Now()
	manager.mu.Lock()
	manager.pruneFailuresLocked(now)
	if manager.pairingLocked || len(manager.failedBySource[source]) >= 5 {
		manager.mu.Unlock()
		http.Error(response, "Pairing attempts are temporarily locked.", http.StatusTooManyRequests)
		return
	}
	offer := manager.offer
	valid := offer != nil && now.Before(offer.expires) &&
		((input.PIN != "" && len(input.PIN) == 4 && subtle.ConstantTimeCompare([]byte(input.PIN), []byte(offer.pin)) == 1) ||
			(input.Token != "" && subtle.ConstantTimeCompare([]byte(input.Token), []byte(offer.token)) == 1))
	if !valid {
		manager.failedBySource[source] = append(manager.failedBySource[source], now)
		manager.failedGlobal = append(manager.failedGlobal, now)
		if len(manager.failedGlobal) >= 20 {
			manager.pairingLocked = true
			manager.offer = nil
		}
		manager.mu.Unlock()
		http.Error(response, "Pairing failed.", http.StatusUnauthorized)
		return
	}
	manager.offer = nil
	manager.mu.Unlock()
	token, err := manager.trust.Issue()
	if err != nil {
		http.Error(response, "Could not persist browser trust.", http.StatusInternalServerError)
		return
	}
	http.SetCookie(response, &http.Cookie{Name: trustCookieName, Value: token, Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
		MaxAge: int(trustAbsolute.Seconds())})
	response.WriteHeader(http.StatusNoContent)
}

func (manager *Manager) logout(response http.ResponseWriter, request *http.Request) {
	if !manager.validControlRequest(request) {
		http.Error(response, "Logout request origin was rejected.", http.StatusForbidden)
		return
	}
	cookie, err := request.Cookie(trustCookieName)
	if err != nil || !manager.trust.Authenticate(cookie.Value) {
		http.Error(response, "This browser is not trusted.", http.StatusUnauthorized)
		return
	}
	if err := manager.trust.Revoke(cookie.Value); err != nil {
		http.Error(response, "Could not revoke browser trust.", http.StatusInternalServerError)
		return
	}
	http.SetCookie(response, &http.Cookie{Name: trustCookieName, Value: "", Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	response.WriteHeader(http.StatusNoContent)
}

func (manager *Manager) validControlRequest(request *http.Request) bool {
	manager.mu.Lock()
	current := manager.instance
	csrf := manager.controlCSRF
	manager.mu.Unlock()
	if current == nil || request.Host != current.host || csrf == "" ||
		subtle.ConstantTimeCompare([]byte(request.Header.Get("X-Leaf-CSRF")), []byte(csrf)) != 1 {
		return false
	}
	expected := "https://" + current.host
	if origin := request.Header.Get("Origin"); origin != "" {
		return origin == expected
	}
	referer, err := url.Parse(request.Header.Get("Referer"))
	return err == nil && referer.Scheme == "https" && referer.Host == current.host
}

func (manager *Manager) authenticated(request *http.Request) bool {
	cookie, err := request.Cookie(trustCookieName)
	return err == nil && manager.trust.Authenticate(cookie.Value)
}

func (manager *Manager) pruneFailuresLocked(now time.Time) {
	manager.failedGlobal = recent(manager.failedGlobal, now.Add(-globalWindow))
	for source, failures := range manager.failedBySource {
		failures = recent(failures, now.Add(-perSourceWindow))
		if len(failures) == 0 {
			delete(manager.failedBySource, source)
		} else {
			manager.failedBySource[source] = failures
		}
	}
}

func recent(values []time.Time, threshold time.Time) []time.Time {
	index := 0
	for index < len(values) && values[index].Before(threshold) {
		index++
	}
	return append([]time.Time(nil), values[index:]...)
}

func secureHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Frame-Options", "DENY")
}

func methodRejected(response http.ResponseWriter) {
	response.Header().Set("Allow", "GET, HEAD")
	http.Error(response, "The Syncthing browser view is read-only.", http.StatusMethodNotAllowed)
}

func hasMethodOverride(header http.Header) bool {
	for _, name := range []string{"X-HTTP-Method-Override", "X-Method-Override", "X-HTTP-Method"} {
		if header.Get(name) != "" {
			return true
		}
	}
	return false
}

func tlsListener(listener net.Listener, certificate tls.Certificate) net.Listener {
	return tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
}
