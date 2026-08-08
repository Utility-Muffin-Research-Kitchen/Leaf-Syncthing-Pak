package syncthing

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyOfflinePauseSetTransactionAndPreservation(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.xml")
	original := `<configuration version="52" future="kept"><folder id="managed-a"><paused>false</paused><future>kept-a</future></folder><folder id="managed-b"><future>kept-b</future></folder><folder id="unmanaged"><paused>false</paused><future>kept-u</future></folder><gui enabled="true"><apikey>secret</apikey></gui></configuration>`
	if err := os.WriteFile(configPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	syncCalls := 0
	result, err := ApplyOfflinePauseSet(directory, map[string]bool{"managed-a": true, "managed-b": true}, func(path string) error {
		if path != directory {
			t.Fatalf("sync path = %s", path)
		}
		syncCalls++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || syncCalls != 1 {
		t.Fatalf("result = %+v, sync calls = %d", result, syncCalls)
	}
	contents, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, fragment := range []string{`future="kept"`, `<future>kept-a</future>`, `<future>kept-b</future>`, `<future>kept-u</future>`, `<apikey>secret</apikey>`} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("promoted config dropped %q: %s", fragment, text)
		}
	}
	if err := verifyPauseSet(configPath, map[string]bool{"managed-a": true, "managed-b": true, "unmanaged": false}); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(filepath.Join(directory, "config.xml.bak"))
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != original {
		t.Fatal("backup is not the exact prior known-good config")
	}
	if _, err := os.Lstat(filepath.Join(directory, "config.xml.tmp")); !os.IsNotExist(err) {
		t.Fatalf("temporary remains: %v", err)
	}
}

func TestApplyOfflinePauseSetNoopReparsesWithoutTransaction(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.xml")
	config := `<configuration version="52"><folder id="managed"><paused>true</paused></folder></configuration>`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := ApplyOfflinePauseSet(directory, map[string]bool{"managed": true}, func(string) error {
		t.Fatal("no-op edit called syncfs")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Fatal("no-op pause set reported a change")
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != config {
		t.Fatalf("no-op changed bytes: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(directory, "config.xml.bak")); !os.IsNotExist(err) {
		t.Fatalf("no-op created backup: %v", err)
	}
}

func TestApplyOfflinePauseSetRefusesMissingOrDuplicateManagedFolder(t *testing.T) {
	for name, config := range map[string]string{
		"missing":   `<configuration version="52"><folder id="other"><paused>false</paused></folder></configuration>`,
		"duplicate": `<configuration version="52"><folder id="managed"></folder><folder id="managed"></folder></configuration>`,
	} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.WriteFile(filepath.Join(directory, "config.xml"), []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := ApplyOfflinePauseSet(directory, map[string]bool{"managed": true}, func(string) error { return nil }); err == nil {
				t.Fatal("unsafe managed folder set was accepted")
			}
		})
	}
}

func TestApplyOfflinePauseSetEmptySetStillValidatesConfig(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "config.xml"), []byte(`<configuration>`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyOfflinePauseSet(directory, nil, func(string) error { return nil }); err == nil {
		t.Fatal("empty pause set accepted an unparseable config")
	}
}
