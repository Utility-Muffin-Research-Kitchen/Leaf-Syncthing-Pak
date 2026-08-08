package controller

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
)

func TestFolderControlStatePersistsManualAndPendingReasons(t *testing.T) {
	path := filepath.Join(t.TempDir(), folderControlStateName)
	folder := syncthing.ConfiguredFolder{ID: "leaf-saves-0011223344556677"}
	store, err := newFolderControlStore(path, []syncthing.ConfiguredFolder{folder})
	if err != nil {
		t.Fatal(err)
	}
	initial := store.Snapshot()[folder.ID]
	if !initial.FirstSync || initial.Manual || initial.PendingRescan {
		t.Fatalf("initial state = %+v", initial)
	}
	if err := store.SetManual(folder.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPendingRescan(folder.ID, true); err != nil {
		t.Fatal(err)
	}
	reloaded, err := newFolderControlStore(path, []syncthing.ConfiguredFolder{folder})
	if err != nil {
		t.Fatal(err)
	}
	got := reloaded.Snapshot()[folder.ID]
	if !got.FirstSync || !got.Manual || !got.PendingRescan {
		t.Fatalf("reloaded state = %+v", got)
	}

	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		t.Fatalf("state file = %v, %v", info, err)
	}
}

func TestFolderControlStateRejectsUnsafeOrUnknownState(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, folderControlStateName)
	if err := os.Symlink(filepath.Join(directory, "target"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := newFolderControlStore(path, nil); err == nil {
		t.Fatal("symlink state was accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema":1,"folders":{"../escape":{"manual":true,"first_sync":true,"pending_rescan":false}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newFolderControlStore(path, nil); err == nil {
		t.Fatal("unsafe folder id was accepted")
	}
}
