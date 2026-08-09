package controller

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/cards"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/leaf"
)

func TestStorageInventoryReportsSnapshotAndVersionSizes(t *testing.T) {
	root := t.TempDir()
	userdata := filepath.Join(root, ".userdata", "mlp1")
	state := filepath.Join(userdata, leaf.AppStateName)
	files := map[string]string{
		filepath.Join(state, "snapshots", "saves", "2026-08-08", "save.srm"): "1234",
		filepath.Join(state, "versions", "states", "state.1"):                "123456",
	}
	for path, payload := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	status, err := storageInventory([]cards.Card{{
		Source: cardsSource(root, userdata), Identity: cards.Identity{Version: 1, ID: "00112233445566778899aabbccddeeff"},
		State: cards.StateEnrolled, Present: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if status.SnapshotBytes != 4 || status.VersionBytes != 6 || status.SnapshotCount != 1 || status.VersionGroups != 1 || len(status.Inventory) != 2 {
		t.Fatalf("storage inventory = %+v", status)
	}
}

func cardsSource(root, userdata string) leaf.Source {
	return leaf.Source{ID: "primary", Root: root, Primary: true, UserdataPath: userdata}
}
