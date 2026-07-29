package main

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCertificateUsesRTCIndependentWindowAndPersists(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "gateway.crt")
	keyFile := filepath.Join(dir, "gateway.key")
	if err := ensureCertificate(certFile, keyFile); err != nil {
		t.Fatal(err)
	}
	firstCert, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(firstCert)
	if block == nil {
		t.Fatal("certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !cert.NotBefore.Equal(certNotBefore) || !cert.NotAfter.Equal(certNotAfter) {
		t.Fatalf("unexpected validity: %s to %s", cert.NotBefore, cert.NotAfter)
	}
	if err := ensureCertificate(certFile, keyFile); err != nil {
		t.Fatal(err)
	}
	secondCert, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstCert, secondCert) {
		t.Fatal("persisted certificate changed")
	}
	info, err := os.Stat(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("private-key mode = %o, want 600", got)
	}
}

func TestMethodRejected(t *testing.T) {
	recorder := httptest.NewRecorder()
	methodRejected(recorder)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
	if got := recorder.Header().Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("Allow = %q", got)
	}
}
