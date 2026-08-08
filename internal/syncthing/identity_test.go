package syncthing

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

func TestEnsureIdentityDiscardsOrphanStagingDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell")
	}
	root := t.TempDir()
	for _, name := range []string{"config.generate.tmp", "data.generate.tmp", "config.migrate.tmp", "data.migrate.tmp"} {
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

func TestEnsureIdentityMigratesOldConfigWithoutReplacingIdentity(t *testing.T) {
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
	configPath := filepath.Join(root, "config", "config.xml")
	contents, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	older := strings.Replace(string(contents), `version="52"`, `version="51"`, 1)
	older = strings.Replace(older, `</configuration>`, `<future-field keep="yes"></future-field></configuration>`, 1)
	if err := os.WriteFile(configPath, []byte(older), 0o600); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(root, "config", GenerationMarkerName)
	marker, err := readGenerationMarker(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	marker.UpstreamVersion = "v2.0.0"
	marker.ConfigVersion = 51
	if err := os.Remove(markerPath); err != nil {
		t.Fatal(err)
	}
	if err := writeGenerationMarker(markerPath, marker); err != nil {
		t.Fatal(err)
	}
	certificateSHA, err := hashIdentityFile(filepath.Join(root, "config", "cert.pem"))
	if err != nil {
		t.Fatal(err)
	}
	keySHA, err := hashIdentityFile(filepath.Join(root, "config", "key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	syncCalls := 0
	options.SyncFilesystem = func(path string) error {
		if path != filepath.Join(root, "config") {
			t.Fatalf("migration sync path = %s", path)
		}
		syncCalls++
		return nil
	}
	migrated, err := EnsureIdentity(context.Background(), options, RecoveryResult{State: RecoveryReady})
	if err != nil {
		t.Fatal(err)
	}
	if migrated.DeviceID != testDeviceID || migrated.UpstreamVersion != "v2.1.2" || migrated.ConfigVersion != 52 || syncCalls != 2 {
		t.Fatalf("migrated identity = %+v, sync calls = %d", migrated, syncCalls)
	}
	if current, err := os.ReadFile(configPath); err != nil || !strings.Contains(string(current), `future-field keep="yes"`) {
		t.Fatalf("migrated config lost an unknown field: %v", err)
	}
	if backup, err := os.ReadFile(filepath.Join(root, "config", "config.xml.bak")); err != nil || !strings.Contains(string(backup), `version="51"`) {
		t.Fatalf("migration backup is not the old config: %v", err)
	}
	if got, err := hashIdentityFile(filepath.Join(root, "config", "cert.pem")); err != nil || got != certificateSHA {
		t.Fatalf("certificate changed: %s (%v)", got, err)
	}
	if got, err := hashIdentityFile(filepath.Join(root, "config", "key.pem")); err != nil || got != keySHA {
		t.Fatalf("private key changed: %s (%v)", got, err)
	}
	updatedMarker, err := readGenerationMarker(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	if updatedMarker.UpstreamVersion != "v2.1.2" || updatedMarker.ConfigVersion != 52 || updatedMarker.CertificateSHA != certificateSHA || updatedMarker.PrivateKeySHA != keySHA {
		t.Fatalf("updated marker = %+v", updatedMarker)
	}
	for _, name := range []string{"config.migrate.tmp", "data.migrate.tmp"} {
		if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("migration staging remains: %s (%v)", name, err)
		}
	}
	options.SyncFilesystem = func(string) error {
		t.Fatal("steady validation unexpectedly called syncfs")
		return nil
	}
	if _, err := EnsureIdentity(context.Background(), options, RecoveryResult{State: RecoveryReady}); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureIdentityRefusesMigrationThatChangesIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell")
	}
	root := t.TempDir()
	options := IdentityOptions{
		Binary: writeFakeSyncthing(t), ConfigDir: filepath.Join(root, "config"),
		DataDir: filepath.Join(root, "data"), UpstreamVersion: "v2.1.2",
		GUISocket: filepath.Join(root, "runtime", "syncthing-gui.sock"),
	}
	if _, err := EnsureIdentity(context.Background(), options, RecoveryResult{State: RecoveryClean}); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config", "config.xml")
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	config = []byte(strings.Replace(string(config), `version="52"`, `version="51"`, 1))
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(root, "config", GenerationMarkerName)
	marker, err := readGenerationMarker(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	marker.UpstreamVersion = "v2.0.0"
	marker.ConfigVersion = 51
	if err := os.Remove(markerPath); err != nil {
		t.Fatal(err)
	}
	if err := writeGenerationMarker(markerPath, marker); err != nil {
		t.Fatal(err)
	}
	before := make(map[string][]byte)
	for _, name := range []string{"config.xml", "cert.pem", "key.pem", GenerationMarkerName} {
		before[name], err = os.ReadFile(filepath.Join(root, "config", name))
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("LEAF_SYNCTHING_FAKE_REPLACE_IDENTITY", "1")
	if _, err := EnsureIdentity(context.Background(), options, RecoveryResult{State: RecoveryReady}); err == nil || !strings.Contains(err.Error(), "migration changed") {
		t.Fatalf("identity-changing migration error = %v", err)
	}
	for name, want := range before {
		got, err := os.ReadFile(filepath.Join(root, "config", name))
		if err != nil || string(got) != string(want) {
			t.Fatalf("real %s changed after refused migration: %v", name, err)
		}
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

// TestDeviceFactoryIdentityAndProcess is an opt-in MLP1 fixture. It exercises
// the real pinned binary, card syncfs barriers, private GUI readiness, and
// verified upstream shutdown on the card selected by the caller.
func TestDeviceFactoryIdentityAndProcess(t *testing.T) {
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
	runtimeDir := filepath.Join(os.TempDir(), "leaf-syncthing-identity-device-runtime")
	if err := os.RemoveAll(runtimeDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	guiSocket := filepath.Join(runtimeDir, "syncthing-gui.sock")
	options := IdentityOptions{
		Binary: binary, ConfigDir: filepath.Join(root, "config"),
		DataDir: filepath.Join(root, "data"), UpstreamVersion: "v2.1.2",
		GUISocket: guiSocket,
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
	process, err := StartProcess(context.Background(), ProcessOptions{
		Binary: binary, ConfigDir: options.ConfigDir, DataDir: options.DataDir,
		GUISocket: guiSocket, ReadinessTimeout: 15 * time.Second,
		Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := process.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
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
        if test -s "$config/config.xml"; then
            sed 's/version="[0-9][0-9]*"/version="52"/' "$config/config.xml" >"$config/config.xml.next"
            mv "$config/config.xml.next" "$config/config.xml"
        else
            printf '%s\n' '<configuration version="52"><device id="` + testDeviceID + `"></device><gui enabled="true"><address>127.0.0.1:8384</address><apikey>fixture-api-key</apikey></gui><options></options></configuration>' >"$config/config.xml"
        fi
        if test "${LEAF_SYNCTHING_FAKE_REPLACE_IDENTITY:-}" = 1; then
            printf '%s\n' 'replacement-certificate' >"$config/cert.pem"
            printf '%s\n' 'replacement-private-key' >"$config/key.pem"
        else
            test -s "$config/cert.pem" || printf '%s\n' 'fixture-certificate' >"$config/cert.pem"
            test -s "$config/key.pem" || printf '%s\n' 'fixture-private-key' >"$config/key.pem"
        fi
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
