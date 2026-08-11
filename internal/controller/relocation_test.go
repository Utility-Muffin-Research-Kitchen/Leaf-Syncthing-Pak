package controller

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/cards"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/leaf"
	syncthingconfig "github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
)

const (
	relocationSelf = "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"
	relocationPeer = "IIIIIII-JJJJJJJ-KKKKKKK-LLLLLLL-MMMMMMM-NNNNNNN-OOOOOOO-PPPPPPP"
)

func TestRelocateManagedFoldersFollowsUniqueCardIDAcrossMountSwap(t *testing.T) {
	oldRoot := t.TempDir()
	cardRoot := t.TempDir()
	cardID := "00112233445566778899aabbccddeeff"
	_, marker, err := cards.BindingNames(cardID, "saves")
	if err != nil {
		t.Fatal(err)
	}
	expectedPath := filepath.Join(cardRoot, "Saves")
	if err := os.MkdirAll(filepath.Join(expectedPath, marker), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(expectedPath, "on-card.sav"), []byte("new mount"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(oldRoot, "Saves")
	if err := os.MkdirAll(oldPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldPath, "other-card.sav"), []byte("old mount"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := leaf.Source{
		ID: "primary", Root: cardRoot, Primary: true,
		UserdataPath: filepath.Join(cardRoot, ".userdata", "mlp1"),
		SavesPath:    expectedPath, StatesPath: filepath.Join(cardRoot, "States"),
	}
	card := cards.Card{
		Source: source, Identity: cards.Identity{Version: 1, ID: cardID},
		State: cards.StateEnrolled, Present: true, Writable: true,
	}
	folder := syncthingconfig.ConfiguredFolder{
		ID: "ra-saves", Label: "ra-saves", Kind: "saves", Path: oldPath,
		Type: "sendreceive", MarkerName: marker, Paused: true,
		VersioningType: "simple", VersioningFSPath: filepath.Join(oldRoot, "versions", "saves"),
		VersioningFSType: "basic", Devices: []string{relocationSelf, relocationPeer},
	}
	controls := map[string]folderControlRecord{
		folder.ID: {CardID: cardID, Kind: "saves", MarkerName: marker, FirstSyncEpoch: 1},
	}
	upstream := newFakeB3Upstream()
	upstream.folders[folder.ID] = folder
	upstream.paused[folder.ID] = true
	relocated, err := relocateManagedFolders(context.Background(), []syncthingconfig.ConfiguredFolder{folder}, []cards.Card{card}, controls, upstream)
	if err != nil {
		t.Fatal(err)
	}
	expectedVersions := filepath.Join(source.UserdataPath, leaf.AppStateName, "versions", "saves")
	if len(relocated) != 1 || filepath.Clean(relocated[0].Path) != filepath.Clean(expectedPath) ||
		filepath.Clean(relocated[0].VersioningFSPath) != filepath.Clean(expectedVersions) || relocated[0].Paused {
		t.Fatalf("relocated folder = %+v", relocated)
	}
	if len(upstream.pauseCalls) != 2 || !upstream.pauseCalls[0] || upstream.pauseCalls[1] {
		t.Fatalf("pause calls = %v", upstream.pauseCalls)
	}
	if data, err := os.ReadFile(filepath.Join(oldPath, "other-card.sav")); err != nil || string(data) != "old mount" {
		t.Fatalf("old mount data changed: %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(expectedPath, "on-card.sav")); err != nil || string(data) != "new mount" {
		t.Fatalf("enrolled card data changed: %q, %v", data, err)
	}
}

func TestRelocateManagedFoldersRefusesMissingMarkerOrDuplicateID(t *testing.T) {
	root := t.TempDir()
	cardID := "00112233445566778899aabbccddeeff"
	_, marker, err := cards.BindingNames(cardID, "saves")
	if err != nil {
		t.Fatal(err)
	}
	source := leaf.Source{
		ID: "primary", Root: root, Primary: true,
		UserdataPath: filepath.Join(root, ".userdata", "mlp1"),
		SavesPath:    filepath.Join(root, "Saves"), StatesPath: filepath.Join(root, "States"),
	}
	if err := os.MkdirAll(source.SavesPath, 0o700); err != nil {
		t.Fatal(err)
	}
	card := cards.Card{Source: source, Identity: cards.Identity{Version: 1, ID: cardID}, State: cards.StateEnrolled, Present: true, Writable: true}
	folder := syncthingconfig.ConfiguredFolder{
		ID: "ra-saves", Label: "ra-saves", Kind: "saves", Path: filepath.Join(t.TempDir(), "Saves"),
		Type: "sendonly", MarkerName: marker, Paused: true, Devices: []string{relocationSelf, relocationPeer},
	}
	controls := map[string]folderControlRecord{folder.ID: {CardID: cardID, Kind: "saves", MarkerName: marker}}
	for _, inventory := range [][]cards.Card{{card}, {card, card}} {
		upstream := newFakeB3Upstream()
		result, err := relocateManagedFolders(context.Background(), []syncthingconfig.ConfiguredFolder{folder}, inventory, controls, upstream)
		if err != nil {
			t.Fatal(err)
		}
		if result[0].Path != folder.Path || len(upstream.pauseCalls) != 0 {
			t.Fatalf("unsafe relocation changed folder: %+v calls=%v", result[0], upstream.pauseCalls)
		}
	}
}
