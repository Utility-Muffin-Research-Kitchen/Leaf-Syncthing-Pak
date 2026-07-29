// Command b0a-gateway-spike qualifies a read-only HTTPS presentation of the
// pinned Syncthing UI. It is a spike, not the production pairing gateway.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	certNotBefore = time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)
	certNotAfter  = time.Date(2120, time.January, 1, 0, 0, 0, 0, time.UTC)
)

type options struct {
	listen     string
	unixSocket string
	apiKeyFile string
	certFile   string
	keyFile    string
}

type leafStatus struct {
	Variant  string `json:"variant"`
	Upstream string `json:"upstream"`
	ReadOnly bool   `json:"readOnly"`
}

func main() {
	var opts options
	flag.StringVar(&opts.listen, "listen", "127.0.0.1:18443", "HTTPS listen address")
	flag.StringVar(&opts.unixSocket, "upstream-socket", "", "Syncthing GUI Unix socket")
	flag.StringVar(&opts.apiKeyFile, "upstream-api-key-file", "", "file containing the Syncthing API key")
	flag.StringVar(&opts.certFile, "cert", "", "persisted gateway certificate")
	flag.StringVar(&opts.keyFile, "key", "", "persisted gateway private key")
	flag.Parse()

	if err := run(opts); err != nil {
		log.Fatal(err)
	}
}

func run(opts options) error {
	if opts.unixSocket == "" || opts.apiKeyFile == "" || opts.certFile == "" || opts.keyFile == "" {
		return errors.New("-upstream-socket, -upstream-api-key-file, -cert, and -key are required")
	}
	apiKeyRaw, err := os.ReadFile(opts.apiKeyFile)
	if err != nil {
		return fmt.Errorf("read upstream API key: %w", err)
	}
	apiKey := strings.TrimSpace(string(apiKeyRaw))
	if apiKey == "" {
		return errors.New("upstream API key is empty")
	}
	if err := ensureCertificate(opts.certFile, opts.keyFile); err != nil {
		return err
	}

	transport := &http.Transport{
		DisableCompression: false,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", opts.unixSocket)
		},
	}
	upstream, _ := url.Parse("http://syncthing-unix")
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.Transport = transport
	baseDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		baseDirector(req)
		req.Host = "syncthing-unix"
		req.Header.Del("X-API-Key")
		req.Header.Del("Authorization")
		req.Header.Set("X-API-Key", apiKey)
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Del("Set-Cookie")
		resp.Header.Set("Cache-Control", "no-store")
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		log.Printf("upstream error: %v", err)
		http.Error(w, "Syncthing status is temporarily unavailable", http.StatusBadGateway)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if req.URL.Path == "/leaf/status" {
			if req.Method != http.MethodGet && req.Method != http.MethodHead {
				methodRejected(w)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(leafStatus{
				Variant:  "upstream-read-only-proxy",
				Upstream: "syncthing-v2.1.2",
				ReadOnly: true,
			})
			return
		}
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			methodRejected(w)
			return
		}
		proxy.ServeHTTP(w, req)
	})

	server := &http.Server{
		Addr:              opts.listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	log.Printf("read-only HTTPS gateway listening on %s", opts.listen)
	return server.ListenAndServeTLS(opts.certFile, opts.keyFile)
}

func methodRejected(w http.ResponseWriter) {
	w.Header().Set("Allow", "GET, HEAD")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusMethodNotAllowed)
	_, _ = w.Write([]byte(`{"error":"read-only gateway: upstream mutations are disabled"}` + "\n"))
}

func ensureCertificate(certFile, keyFile string) error {
	certExists := fileExists(certFile)
	keyExists := fileExists(keyFile)
	if certExists != keyExists {
		return errors.New("gateway certificate and key must either both exist or both be absent")
	}
	if certExists {
		_, err := tlsLoadPair(certFile, keyFile)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(certFile), 0o700); err != nil {
		return fmt.Errorf("create certificate directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
		return fmt.Errorf("create key directory: %w", err)
	}

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate private key: %w", err)
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return fmt.Errorf("generate serial: %w", err)
	}
	template := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "Leaf Syncthing Gateway",
			Organization: []string{"Utility Muffin Research Kitchen"},
		},
		NotBefore:             certNotBefore,
		NotAfter:              certNotAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return fmt.Errorf("create certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return fmt.Errorf("marshal private key: %w", err)
	}
	if err := writeExclusive(certFile, 0o644, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); err != nil {
		return err
	}
	if err := writeExclusive(keyFile, 0o600, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})); err != nil {
		return err
	}
	_, err = tlsLoadPair(certFile, keyFile)
	return err
}

func tlsLoadPair(certFile, keyFile string) (*x509.Certificate, error) {
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load certificate pair: %w", err)
	}
	if len(pair.Certificate) == 0 {
		return nil, errors.New("gateway certificate chain is empty")
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	if cert.NotBefore != certNotBefore || cert.NotAfter != certNotAfter {
		return nil, errors.New("gateway certificate has unexpected validity window")
	}
	return cert, nil
}

func writeExclusive(path string, mode os.FileMode, contents []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(contents); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return f.Sync()
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
