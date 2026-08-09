package gateway

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var (
	certificateNotBefore = time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)
	certificateNotAfter  = time.Date(2120, time.January, 1, 0, 0, 0, 0, time.UTC)
)

type certificateResult struct {
	Certificate tls.Certificate
	Fingerprint string
	CertPath    string
	KeyPath     string
}

func ensureCertificate(directory string, addresses []net.IP) (certificateResult, error) {
	if !filepath.IsAbs(directory) || len(addresses) == 0 {
		return certificateResult{}, errors.New("gateway certificate requires an absolute directory and addresses")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return certificateResult{}, fmt.Errorf("create gateway state: %w", err)
	}
	certPath := filepath.Join(directory, "gateway-cert.pem")
	keyPath := filepath.Join(directory, "gateway-key.pem")
	if result, err := loadCertificate(certPath, keyPath, addresses); err == nil {
		return result, nil
	}
	certPEM, keyPEM, err := generateCertificate(addresses)
	if err != nil {
		return certificateResult{}, err
	}
	if err := replacePair(certPath, keyPath, certPEM, keyPEM); err != nil {
		return certificateResult{}, err
	}
	return loadCertificate(certPath, keyPath, addresses)
}

func generateCertificate(addresses []net.IP) ([]byte, []byte, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, nil, err
	}
	template := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{CommonName: "Leaf Syncthing Gateway",
			Organization: []string{"Utility Muffin Research Kitchen"}},
		NotBefore: certificateNotBefore, NotAfter: certificateNotAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           normalizedIPs(addresses),
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

func loadCertificate(certPath, keyPath string, addresses []net.IP) (certificateResult, error) {
	for _, path := range []string{certPath, keyPath} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return certificateResult{}, errors.New("gateway certificate pair is unsafe")
		}
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil || len(pair.Certificate) == 0 {
		return certificateResult{}, errors.New("gateway certificate pair is unavailable")
	}
	certificate, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return certificateResult{}, err
	}
	if !certificate.NotBefore.Equal(certificateNotBefore) || !certificate.NotAfter.Equal(certificateNotAfter) ||
		!sameIPs(certificate.IPAddresses, addresses) {
		return certificateResult{}, errors.New("gateway certificate address set changed")
	}
	digest := sha256.Sum256(certificate.Raw)
	encoded := strings.ToUpper(hex.EncodeToString(digest[:]))
	parts := make([]string, 0, len(encoded)/2)
	for index := 0; index < len(encoded); index += 2 {
		parts = append(parts, encoded[index:index+2])
	}
	return certificateResult{Certificate: pair, Fingerprint: strings.Join(parts, ":"),
		CertPath: certPath, KeyPath: keyPath}, nil
}

func normalizedIPs(addresses []net.IP) []net.IP {
	values := make([]net.IP, 0, len(addresses))
	seen := map[string]struct{}{}
	for _, address := range addresses {
		if address == nil {
			continue
		}
		value := address.String()
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, append(net.IP(nil), address...))
	}
	sort.Slice(values, func(left, right int) bool { return values[left].String() < values[right].String() })
	return values
}

func sameIPs(left, right []net.IP) bool {
	left = normalizedIPs(left)
	right = normalizedIPs(right)
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !left[index].Equal(right[index]) {
			return false
		}
	}
	return true
}

func replacePair(certPath, keyPath string, certPEM, keyPEM []byte) error {
	for _, file := range []struct {
		path string
		mode os.FileMode
		data []byte
	}{{certPath + ".tmp", 0o644, certPEM}, {keyPath + ".tmp", 0o600, keyPEM}} {
		if info, err := os.Lstat(file.path); err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return errors.New("gateway certificate temporary is unsafe")
			}
			if err := os.Remove(file.path); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		output, err := os.OpenFile(file.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, file.mode)
		if err != nil {
			return err
		}
		if _, err := output.Write(file.data); err != nil {
			_ = output.Close()
			return err
		}
		if err := output.Sync(); err != nil {
			_ = output.Close()
			return err
		}
		if err := output.Close(); err != nil {
			return err
		}
	}
	if err := os.Rename(keyPath+".tmp", keyPath); err != nil {
		return err
	}
	if err := os.Rename(certPath+".tmp", certPath); err != nil {
		return err
	}
	return nil
}
