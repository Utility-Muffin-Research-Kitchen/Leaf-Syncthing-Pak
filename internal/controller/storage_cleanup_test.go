package controller

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/cards"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/leaf"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/uicontrol"
)

func TestCleanupStorageSnapshotRetainsLiveTreeAndVersionHistory(t *testing.T) {
	fixture := newFirstSyncFixture(t, "sendreceive")
	if err := fixture.controls.SetFirstSync(fixture.folder.ID, false); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(fixture.card.Source.UserdataPath, leaf.AppStateName)
	snapshot := filepath.Join(stateRoot, "snapshots", "saves", "first-sync-test")
	version := filepath.Join(stateRoot, "versions", "saves", "slot.sav~1")
	writeCleanupFile(t, filepath.Join(snapshot, "slot.sav"), "copy")
	writeCleanupFile(t, version, "history")

	if err := cleanupStorageRow(
		[]cards.Card{fixture.card}, uicontrol.Status{}, fixture.controls.Snapshot(),
		identitySuffix(fixture.card.Identity.ID), "snapshot", "saves", "first-sync-test", 4,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(snapshot); !os.IsNotExist(err) {
		t.Fatalf("snapshot still exists: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(fixture.folder.Path, "slot.sav")); err != nil || string(got) != "save-data" {
		t.Fatalf("live save = %q, %v", got, err)
	}
	if got, err := os.ReadFile(version); err != nil || string(got) != "history" {
		t.Fatalf("version history = %q, %v", got, err)
	}
}

func TestCleanupStorageVersionHistoryRequiresPausedManagedFolder(t *testing.T) {
	fixture := newFirstSyncFixture(t, "sendreceive")
	versionRoot := filepath.Join(fixture.card.Source.UserdataPath, leaf.AppStateName, "versions", "saves")
	version := filepath.Join(versionRoot, "slot.sav~1")
	writeCleanupFile(t, version, "history")
	status := uicontrol.Status{Folders: []uicontrol.FolderStatus{{ID: fixture.folder.ID}}}

	err := cleanupStorageRow(
		[]cards.Card{fixture.card}, status, fixture.controls.Snapshot(),
		identitySuffix(fixture.card.Identity.ID), "versions", "saves", "Saves version history", 7,
	)
	if err == nil || !strings.Contains(err.Error(), "pause the managed folder") {
		t.Fatalf("active version cleanup error = %v", err)
	}
	if _, err := os.Stat(version); err != nil {
		t.Fatalf("rejected cleanup changed history: %v", err)
	}

	status.Folders[0].Paused = true
	if err := cleanupStorageRow(
		[]cards.Card{fixture.card}, status, fixture.controls.Snapshot(),
		identitySuffix(fixture.card.Identity.ID), "versions", "saves", "Saves version history", 7,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(versionRoot); !os.IsNotExist(err) {
		t.Fatalf("version root still exists: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(fixture.folder.Path, "slot.sav")); err != nil || string(got) != "save-data" {
		t.Fatalf("live save = %q, %v", got, err)
	}
}

func TestCleanupStorageRejectsFirstSyncStaleSizeAndSymlinks(t *testing.T) {
	fixture := newFirstSyncFixture(t, "sendreceive")
	snapshot := filepath.Join(fixture.card.Source.UserdataPath, leaf.AppStateName, "snapshots", "saves", "protected")
	file := filepath.Join(snapshot, "slot.sav")
	writeCleanupFile(t, file, "copy")
	cardSuffix := identitySuffix(fixture.card.Identity.ID)

	err := cleanupStorageRow(
		[]cards.Card{fixture.card}, uicontrol.Status{}, fixture.controls.Snapshot(),
		cardSuffix, "snapshot", "saves", "protected", 4,
	)
	if err == nil || !strings.Contains(err.Error(), "first-sync protection") {
		t.Fatalf("first-sync cleanup error = %v", err)
	}
	if err := fixture.controls.SetFirstSync(fixture.folder.ID, false); err != nil {
		t.Fatal(err)
	}
	err = cleanupStorageRow(
		[]cards.Card{fixture.card}, uicontrol.Status{}, fixture.controls.Snapshot(),
		cardSuffix, "snapshot", "saves", "protected", 3,
	)
	if err == nil || !strings.Contains(err.Error(), "changed after confirmation") {
		t.Fatalf("stale-size cleanup error = %v", err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("rejected cleanup changed snapshot: %v", err)
	}

	if err := os.Symlink(file, filepath.Join(snapshot, "link")); err != nil {
		t.Fatal(err)
	}
	if err := cleanupStorageRow(
		[]cards.Card{fixture.card}, uicontrol.Status{}, fixture.controls.Snapshot(),
		cardSuffix, "snapshot", "saves", "protected", 4,
	); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink cleanup error = %v", err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("symlink rejection changed snapshot: %v", err)
	}
}

func writeCleanupFile(t *testing.T, path, payload string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
}
