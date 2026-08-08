package gateway

import (
	"net"
	"os"
	"testing"
)

func TestCertificatePersistsForAddressSetAndRotatesOnChange(t *testing.T) {
	directory := t.TempDir()
	first, err := ensureCertificate(directory, []net.IP{net.ParseIP("192.0.2.10"), net.ParseIP("2001:db8::10")})
	if err != nil {
		t.Fatal(err)
	}
	second, err := ensureCertificate(directory, []net.IP{net.ParseIP("2001:db8::10"), net.ParseIP("192.0.2.10")})
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint != second.Fingerprint {
		t.Fatal("certificate changed for the same normalized address set")
	}
	rotated, err := ensureCertificate(directory, []net.IP{net.ParseIP("192.0.2.11")})
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint == rotated.Fingerprint {
		t.Fatal("certificate did not rotate after address change")
	}
	certificate, err := loadCertificate(rotated.CertPath, rotated.KeyPath, []net.IP{net.ParseIP("192.0.2.11")})
	if err != nil || certificate.Certificate.Leaf != nil {
		// tls.LoadX509KeyPair does not populate Leaf; successful strict loading is what matters.
		if err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(rotated.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode = %v", info.Mode().Perm())
	}
}

func TestLoadCertificateRejectsSymlinkPair(t *testing.T) {
	directory := t.TempDir()
	result, err := ensureCertificate(directory, []net.IP{net.ParseIP("192.0.2.10")})
	if err != nil {
		t.Fatal(err)
	}
	linkedKey := result.KeyPath + ".link"
	if err := os.Symlink(result.KeyPath, linkedKey); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCertificate(result.CertPath, linkedKey, []net.IP{net.ParseIP("192.0.2.10")}); err == nil {
		t.Fatal("symlinked private key was accepted")
	}
}
