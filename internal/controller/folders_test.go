package controller

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/cards"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/leaf"
	syncthingconfig "github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/uicontrol"
)

func TestReconcileManagedFoldersRequiresPhysicalBindingAndCustomMarker(t *testing.T) {
	root := t.TempDir()
	cardID := "00112233445566778899aabbccddeeff"
	folderID, marker, err := cards.BindingNames(cardID, "saves")
	if err != nil {
		t.Fatal(err)
	}
	saves := filepath.Join(root, "Saves")
	if err := os.MkdirAll(filepath.Join(saves, marker), 0o700); err != nil {
		t.Fatal(err)
	}
	source := leaf.Source{
		ID: "primary", Root: root, Primary: true,
		UserdataPath: filepath.Join(root, ".userdata", "mlp1"), SavesPath: saves,
		StatesPath: filepath.Join(root, "States"),
	}
	card := cards.Card{
		Source: source, Identity: cards.Identity{Version: 1, ID: cardID},
		State: cards.StateEnrolled, Present: true, Writable: true,
	}
	folder := syncthingconfig.ConfiguredFolder{
		ID: folderID, Kind: "saves", Path: saves, Type: "sendonly", MarkerName: marker, Paused: true,
	}
	control := map[string]folderControlRecord{
		folder.ID: {CardID: cardID, Kind: "saves", MarkerName: marker, FirstSync: true, FirstSyncEpoch: 1},
	}
	rows, issues := reconcileManagedFolders([]syncthingconfig.ConfiguredFolder{folder}, []cards.Card{card}, control)
	if len(rows) != 1 || len(issues) != 0 || rows[0].State != "paused" || !rows[0].Paused ||
		len(rows[0].PauseReasons) != 1 || rows[0].PauseReasons[0] != "first-sync" || rows[0].CardID != cardID {
		t.Fatalf("safe binding = %+v, issues=%+v", rows, issues)
	}

	if err := os.Mkdir(filepath.Join(saves, ".stfolder"), 0o700); err != nil {
		t.Fatal(err)
	}
	rows, issues = reconcileManagedFolders([]syncthingconfig.ConfiguredFolder{folder}, []cards.Card{card}, control)
	if len(issues) != 1 || issues[0].Code != "foreign-folder-manager" || rows[0].State != "error" {
		t.Fatalf("foreign binding = %+v, issues=%+v", rows, issues)
	}
}

func TestReconcileManagedFoldersRejectsReceiveOnReadOnlyOrWrongCard(t *testing.T) {
	root := t.TempDir()
	cardID := "00112233445566778899aabbccddeeff"
	folderID, marker, err := cards.BindingNames(cardID, "states")
	if err != nil {
		t.Fatal(err)
	}
	states := filepath.Join(root, "States")
	if err := os.MkdirAll(filepath.Join(states, marker), 0o700); err != nil {
		t.Fatal(err)
	}
	source := leaf.Source{
		ID: "primary", Root: root, Primary: true,
		UserdataPath: filepath.Join(root, ".userdata", "mlp1"), SavesPath: filepath.Join(root, "Saves"), StatesPath: states,
	}
	card := cards.Card{
		Source: source, Identity: cards.Identity{Version: 1, ID: cardID},
		State: cards.StateEnrolled, Present: true, Writable: false,
	}
	folder := syncthingconfig.ConfiguredFolder{
		ID: folderID, Kind: "states", Path: filepath.Join(root, "wrong"), Type: "sendreceive", MarkerName: marker,
		VersioningType: "simple", VersioningFSPath: filepath.Join(source.UserdataPath, leaf.AppStateName, "versions", "states"), VersioningFSType: "basic",
	}
	control := map[string]folderControlRecord{
		folder.ID: {CardID: cardID, Kind: "states", MarkerName: marker, FirstSync: true, FirstSyncEpoch: 1},
	}
	rows, issues := reconcileManagedFolders([]syncthingconfig.ConfiguredFolder{folder}, []cards.Card{card}, control)
	if len(rows) != 1 || rows[0].State != "error" || !hasFolderIssue(issues, "unsafe-folder-path") || !hasFolderIssue(issues, "card-read-only") {
		t.Fatalf("unsafe receive binding = %+v, issues=%+v", rows, issues)
	}
}

func TestReconcileManagedFoldersUsesDurableBindingForExternalID(t *testing.T) {
	root := t.TempDir()
	cardID := "00112233445566778899aabbccddeeff"
	_, marker, err := cards.BindingNames(cardID, "saves")
	if err != nil {
		t.Fatal(err)
	}
	saves := filepath.Join(root, "Saves")
	if err := os.MkdirAll(filepath.Join(saves, marker), 0o700); err != nil {
		t.Fatal(err)
	}
	source := leaf.Source{
		ID: "primary", Root: root, Primary: true,
		UserdataPath: filepath.Join(root, ".userdata", "mlp1"), SavesPath: saves,
		StatesPath: filepath.Join(root, "States"),
	}
	card := cards.Card{
		Source: source, Identity: cards.Identity{Version: 1, ID: cardID},
		State: cards.StateEnrolled, Present: true, Writable: true,
	}
	folder := syncthingconfig.ConfiguredFolder{
		ID: "retro-saves", Kind: "saves", Path: saves, Type: "sendonly", MarkerName: marker, Paused: true,
	}
	control := map[string]folderControlRecord{
		folder.ID: {CardID: cardID, Kind: "saves", MarkerName: marker, FirstSync: true, FirstSyncEpoch: 1},
	}
	rows, issues := reconcileManagedFolders([]syncthingconfig.ConfiguredFolder{folder}, []cards.Card{card}, control)
	if len(rows) != 1 || len(issues) != 0 || rows[0].CardID != cardID || !rows[0].Paused {
		t.Fatalf("external binding = %+v, issues=%+v", rows, issues)
	}
}

func hasFolderIssue(issues []uicontrol.Issue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
