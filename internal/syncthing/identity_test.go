package syncthing

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const testDeviceID = "J5FPDWX-3DWTAGS-6V5DOUL-F66RROL-MC4S7KR-CZTY353-6VLPHUJ-QN7ULA6"

func TestEnsureIdentityGeneratesPromotesAndRevalidates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell")
	}
	root := t.TempDir()
	binary := writeFakeSyncthing(t)
	options := IdentityOptions{
		Binary: binary, ConfigDir: filepath.Join(root, "config"),
		DataDir: filepath.Join(root, "data"), UpstreamVersion: "v2.1.2",
		GUISocket: filepath.Join(root, "runtime", "syncthing-gui.sock"),
	}
	syncCalls := 0
	options.SyncFilesystem = func(path string) error {
		if path != root {
			t.Fatalf("sync path = %s, want %s", path, root)
		}
		syncCalls++
		return nil
	}

	identity, err := EnsureIdentity(context.Background(), options, RecoveryResult{State: RecoveryClean})
	if err != nil {
		t.Fatal(err)
	}
	if identity.DeviceID != testDeviceID || identity.ConfigVersion != 52 || syncCalls != 2 {
		t.Fatalf("identity = %+v, sync calls = %d", identity, syncCalls)
	}
	for _, path := range []string{
		filepath.Join(root, "config", "config.xml"),
		filepath.Join(root, "config", "cert.pem"),
		filepath.Join(root, "config", "key.pem"),
		filepath.Join(root, "config", GenerationMarkerName),
	} {
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("promoted identity file missing: %s (%v)", path, err)
		}
	}
	for _, path := range []string{filepath.Join(root, "config.generate.tmp"), filepath.Join(root, "data.generate.tmp")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("temporary remains: %s (%v)", path, err)
		}
	}

	options.SyncFilesystem = func(string) error {
		t.Fatal("steady validation unexpectedly called syncfs")
		return nil
	}
	validated, err := EnsureIdentity(context.Background(), options, RecoveryResult{State: RecoveryReady})
	if err != nil {
		t.Fatal(err)
	}
	if validated != identity {
		t.Fatalf("validated identity = %+v, want %+v", validated, identity)
	}
}

func TestEnsureIdentityDiscardsOrphanGenerationDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell")
	}
	root := t.TempDir()
	for _, name := range []string{"config.generate.tmp", "data.generate.tmp"} {
		path := filepath.Join(root, name)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "stale"), []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	options := IdentityOptions{
		Binary: writeFakeSyncthing(t), ConfigDir: filepath.Join(root, "config"),
		DataDir: filepath.Join(root, "data"), UpstreamVersion: "v2.1.2",
		GUISocket:      filepath.Join(root, "runtime", "syncthing-gui.sock"),
		SyncFilesystem: func(string) error { return nil },
	}
	if _, err := EnsureIdentity(context.Background(), options, RecoveryResult{State: RecoveryClean}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "config", "stale")); !os.IsNotExist(err) {
		t.Fatalf("orphan content was promoted: %v", err)
	}
}

func TestEnsureIdentityRefusesMarkerMismatchAndOldVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell")
	}
	root := t.TempDir()
	options := IdentityOptions{
		Binary: writeFakeSyncthing(t), ConfigDir: filepath.Join(root, "config"),
		DataDir: filepath.Join(root, "data"), UpstreamVersion: "v2.1.2",
		GUISocket:      filepath.Join(root, "runtime", "syncthing-gui.sock"),
		SyncFilesystem: func(string) error { return nil },
	}
	if _, err := EnsureIdentity(context.Background(), options, RecoveryResult{State: RecoveryClean}); err != nil {
		t.Fatal(err)
	}

	options.UpstreamVersion = "v2.1.3"
	if _, err := EnsureIdentity(context.Background(), options, RecoveryResult{State: RecoveryReady}); !errors.Is(err, ErrMigrationRequired) {
		t.Fatalf("version mismatch error = %v, want %v", err, ErrMigrationRequired)
	}
	options.UpstreamVersion = "v2.1.2"
	configPath := filepath.Join(root, "config", "config.xml")
	contents, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	older := strings.Replace(string(contents), `version="52"`, `version="51"`, 1)
	if err := os.WriteFile(configPath, []byte(older), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureIdentity(context.Background(), options, RecoveryResult{State: RecoveryReady}); !errors.Is(err, ErrMigrationRequired) {
		t.Fatalf("schema mismatch error = %v, want %v", err, ErrMigrationRequired)
	}
	if err := os.WriteFile(configPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "key.pem"), []byte("changed-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureIdentity(context.Background(), options, RecoveryResult{State: RecoveryReady}); err == nil {
		t.Fatal("identity validation accepted a changed private key")
	}
}

func TestEnsureIdentityRejectsSymlinkedGeneratedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell")
	}
	root := t.TempDir()
	options := IdentityOptions{
		Binary: writeFakeSyncthing(t), ConfigDir: filepath.Join(root, "config"),
		DataDir: filepath.Join(root, "data"), UpstreamVersion: "v2.1.2",
		GUISocket:      filepath.Join(root, "runtime", "syncthing-gui.sock"),
		SyncFilesystem: func(string) error { return nil },
	}
	if _, err := EnsureIdentity(context.Background(), options, RecoveryResult{State: RecoveryClean}); err != nil {
		t.Fatal(err)
	}
	certificate := filepath.Join(root, "config", "cert.pem")
	target := filepath.Join(t.TempDir(), "outside-cert.pem")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(certificate); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, certificate); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureIdentity(context.Background(), options, RecoveryResult{State: RecoveryReady}); err == nil {
		t.Fatal("identity validation accepted a symlinked certificate")
	}
}

func TestEnsureIdentityRefusesExistingFactoryConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	options := IdentityOptions{
		Binary: writeFakeSyncthing(t), ConfigDir: filepath.Join(root, "config"),
		DataDir: filepath.Join(root, "data"), UpstreamVersion: "v2.1.2",
		GUISocket:      filepath.Join(root, "runtime", "syncthing-gui.sock"),
		SyncFilesystem: func(string) error { return nil },
	}
	if _, err := EnsureIdentity(context.Background(), options, RecoveryResult{State: RecoveryClean}); err == nil {
		t.Fatal("factory generation accepted an existing config directory")
	}
}

// TestDeviceFactoryIdentity is an opt-in MLP1 fixture. It exercises the real
// pinned binary and real syncfs barriers on the card selected by the caller.
func TestDeviceFactoryIdentity(t *testing.T) {
	root := os.Getenv("LEAF_SYNCTHING_DEVICE_TEST_ROOT")
	binary := os.Getenv("LEAF_SYNCTHING_DEVICE_BINARY")
	if root == "" && binary == "" {
		t.Skip("device identity fixture not requested")
	}
	if filepath.Base(root) != ".leaf-syncthing-identity-device-test" || binary == "" {
		t.Fatal("device fixture requires an exact .leaf-syncthing-identity-device-test root and binary")
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("device fixture root must not exist: %v", err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if filepath.Base(root) == ".leaf-syncthing-identity-device-test" {
			_ = os.RemoveAll(root)
		}
	})
	if err := os.Mkdir(filepath.Join(root, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	options := IdentityOptions{
		Binary: binary, ConfigDir: filepath.Join(root, "config"),
		DataDir: filepath.Join(root, "data"), UpstreamVersion: "v2.1.2",
		GUISocket: "/tmp/leaf-syncthing-identity-device-test/syncthing-gui.sock",
	}
	generated, err := EnsureIdentity(context.Background(), options, RecoveryResult{State: RecoveryClean})
	if err != nil {
		t.Fatal(err)
	}
	validated, err := EnsureIdentity(context.Background(), options, RecoveryResult{State: RecoveryReady})
	if err != nil {
		t.Fatal(err)
	}
	if generated != validated || generated.ConfigVersion <= 0 || !deviceIDPattern.MatchString(generated.DeviceID) {
		t.Fatal("real identity did not remain stable across validation")
	}
}

func writeFakeSyncthing(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "syncthing")
	script := `#!/bin/sh
set -eu
config=
data=
command=
for arg in "$@"; do
    case "$arg" in
        --config=*) config=${arg#--config=} ;;
        --data=*) data=${arg#--data=} ;;
        generate|device-id) command=$arg ;;
    esac
done
case "$command" in
    generate)
        mkdir -p "$config" "$data"
        printf '%s\n' '<configuration version="52"><device id="` + testDeviceID + `"></device><gui enabled="true"><address>127.0.0.1:8384</address><apikey>fixture-api-key</apikey></gui><options></options></configuration>' >"$config/config.xml"
        printf '%s\n' 'fixture-certificate' >"$config/cert.pem"
        printf '%s\n' 'fixture-private-key' >"$config/key.pem"
        ;;
    device-id)
        test -s "$config/cert.pem"
        test -s "$config/key.pem"
        printf '%s\n' '` + testDeviceID + `'
        ;;
    *) exit 64 ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
