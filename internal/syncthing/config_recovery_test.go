package syncthing

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const (
	validConfig   = `<configuration version="1"><folder id="leaf"/></configuration>`
	secondConfig  = `<configuration version="2"></configuration>`
	invalidConfig = `<configuration>`
)

func TestRecoverConfigStateTable(t *testing.T) {
	tests := []struct {
		name          string
		files         map[string]string
		wantState     RecoveryState
		wantChanged   bool
		wantConfig    string
		wantTemporary bool
		wantBackup    bool
		wantSyncCalls int
		wantRefusal   bool
	}{
		{
			name: "steady config", files: map[string]string{"config.xml": validConfig},
			wantState: RecoveryReady, wantConfig: validConfig,
		},
		{
			name:      "steady config discards temporary and invalid backup",
			files:     map[string]string{"config.xml": validConfig, "config.xml.tmp": secondConfig, "config.xml.bak": invalidConfig},
			wantState: RecoveryReady, wantChanged: true, wantConfig: validConfig, wantSyncCalls: 1,
		},
		{
			name:      "steady config retains valid backup",
			files:     map[string]string{"config.xml": validConfig, "config.xml.bak": secondConfig},
			wantState: RecoveryReady, wantConfig: validConfig, wantBackup: true,
		},
		{
			name:      "restore between renames",
			files:     map[string]string{"config.xml.tmp": secondConfig, "config.xml.bak": validConfig},
			wantState: RecoveryReady, wantChanged: true, wantConfig: validConfig, wantSyncCalls: 1,
		},
		{
			name:      "restore when promoted name was not durable",
			files:     map[string]string{"config.xml.bak": validConfig},
			wantState: RecoveryReady, wantChanged: true, wantConfig: validConfig, wantSyncCalls: 1,
		},
		{
			name:      "restore over unusable config",
			files:     map[string]string{"config.xml": invalidConfig, "config.xml.tmp": secondConfig, "config.xml.bak": validConfig},
			wantState: RecoveryReady, wantChanged: true, wantConfig: validConfig, wantSyncCalls: 1,
		},
		{
			name:      "clean install discards invalid debris",
			files:     map[string]string{"config.xml": invalidConfig, "config.xml.tmp": secondConfig, "config.xml.bak": invalidConfig},
			wantState: RecoveryClean, wantChanged: true, wantSyncCalls: 1,
		},
		{
			name: "empty clean install", files: map[string]string{},
			wantState: RecoveryClean,
		},
		{
			name:        "identity without known-good config refuses",
			files:       map[string]string{"config.xml": invalidConfig, "cert.pem": "certificate", "key.pem": "private"},
			wantRefusal: true, wantConfig: invalidConfig,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			for name, contents := range test.files {
				if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			syncCalls := 0
			result, err := RecoverConfig(directory, func(string) error {
				syncCalls++
				return nil
			})
			if test.wantRefusal {
				if !errors.Is(err, ErrNoKnownGoodConfig) {
					t.Fatalf("RecoverConfig() error = %v, want %v", err, ErrNoKnownGoodConfig)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !test.wantRefusal && (result.State != test.wantState || result.Changed != test.wantChanged) {
				t.Fatalf("result = %+v, want state=%s changed=%v", result, test.wantState, test.wantChanged)
			}
			if syncCalls != test.wantSyncCalls {
				t.Fatalf("sync calls = %d, want %d", syncCalls, test.wantSyncCalls)
			}
			assertFile(t, filepath.Join(directory, "config.xml"), test.wantConfig)
			assertExists(t, filepath.Join(directory, "config.xml.tmp"), test.wantTemporary)
			assertExists(t, filepath.Join(directory, "config.xml.bak"), test.wantBackup)
		})
	}
}

func TestRecoverConfigRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside.xml")
	if err := os.WriteFile(target, []byte(validConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(directory, "config.xml")); err != nil {
		t.Fatal(err)
	}
	if _, err := RecoverConfig(directory, func(string) error { return nil }); err == nil {
		t.Fatal("RecoverConfig accepted a symlinked config")
	}
}

func TestRecoverConfigConvergesAfterFlushFailure(t *testing.T) {
	tests := []struct {
		name      string
		files     map[string]string
		wantState RecoveryState
	}{
		{
			name:      "steady cleanup",
			files:     map[string]string{"config.xml": validConfig, "config.xml.tmp": secondConfig},
			wantState: RecoveryReady,
		},
		{
			name:      "backup restore",
			files:     map[string]string{"config.xml": invalidConfig, "config.xml.bak": validConfig},
			wantState: RecoveryReady,
		},
		{
			name:      "factory cleanup",
			files:     map[string]string{"config.xml": invalidConfig, "config.xml.tmp": invalidConfig},
			wantState: RecoveryClean,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			for name, contents := range test.files {
				if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			injected := errors.New("injected syncfs failure")
			if _, err := RecoverConfig(directory, func(string) error { return injected }); !errors.Is(err, injected) {
				t.Fatalf("first recovery error = %v, want %v", err, injected)
			}
			result, err := RecoverConfig(directory, func(string) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			if result.State != test.wantState {
				t.Fatalf("converged state = %s, want %s", result.State, test.wantState)
			}
		})
	}
}

func TestValidateXML(t *testing.T) {
	directory := t.TempDir()
	for name, contents := range map[string]string{
		"valid.xml":      validConfig,
		"invalid.xml":    invalidConfig,
		"multiple.xml":   `<one></one><two></two>`,
		"wrong-root.xml": `<not-configuration></not-configuration>`,
		"empty.xml":      "",
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateXML(filepath.Join(directory, "valid.xml")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"invalid.xml", "multiple.xml", "wrong-root.xml", "empty.xml"} {
		if err := ValidateXML(filepath.Join(directory, name)); err == nil {
			t.Fatalf("ValidateXML accepted %s", name)
		}
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if want == "" {
		if !os.IsNotExist(err) {
			t.Fatalf("%s exists unexpectedly or could not be read: %v", path, err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != want {
		t.Fatalf("%s = %q, want %q", path, contents, want)
	}
}

func assertExists(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Lstat(path)
	if want && err != nil {
		t.Fatalf("%s missing: %v", path, err)
	}
	if !want && !os.IsNotExist(err) {
		t.Fatalf("%s exists unexpectedly or stat failed: %v", path, err)
	}
}
