package controller

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanFolderConflictsIsBoundedAndRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{
		"game.sav", "game.sync-conflict-20260808-120000-PEER.sav",
		filepath.Join("nested", "state.sync-conflict-20260808-120001-PEER.state"),
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	paths, total, err := scanFolderConflicts(root)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(paths) != 2 {
		t.Fatalf("conflicts = %d, %#v", total, paths)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "unsafe")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := scanFolderConflicts(root); err == nil {
		t.Fatal("conflict scan accepted a symlink")
	}
}
