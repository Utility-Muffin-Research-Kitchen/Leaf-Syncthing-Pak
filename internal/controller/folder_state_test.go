package controller

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/cards"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
)

func TestFolderControlStatePersistsManualAndPendingReasons(t *testing.T) {
	path := filepath.Join(t.TempDir(), folderControlStateName)
	card := cards.Card{Identity: cards.Identity{Version: 1, ID: "00112233445566778899aabbccddeeff"}}
	folderID, markerName, err := cards.BindingNames(card.Identity.ID, "saves")
	if err != nil {
		t.Fatal(err)
	}
	folder := syncthing.ConfiguredFolder{ID: folderID, Kind: "saves", MarkerName: markerName}
	store, err := newFolderControlStore(path, []syncthing.ConfiguredFolder{folder}, []cards.Card{card})
	if err != nil {
		t.Fatal(err)
	}
	initial := store.Snapshot()[folder.ID]
	if !initial.FirstSync || initial.FirstSyncEpoch != 1 || initial.Manual || initial.PendingRescan {
		t.Fatalf("initial state = %+v", initial)
	}
	if err := store.SetManual(folder.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPendingRescan(folder.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetFirstSync(folder.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := store.RequireFirstSync(folder.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginMembership(folder.ID, onboardingPeer, "share"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := newFolderControlStore(path, []syncthing.ConfiguredFolder{folder}, []cards.Card{card})
	if err != nil {
		t.Fatal(err)
	}
	got := reloaded.Snapshot()[folder.ID]
	if !got.FirstSync || got.FirstSyncEpoch != 2 || !got.Manual || !got.PendingRescan ||
		got.PendingMembership != "share" || got.PendingDeviceID != onboardingPeer {
		t.Fatalf("reloaded state = %+v", got)
	}
	if err := reloaded.CompleteMembership(folder.ID); err != nil {
		t.Fatal(err)
	}
	if record := reloaded.Snapshot()[folder.ID]; record.PendingMembership != "" || record.PendingDeviceID != "" {
		t.Fatalf("completed membership state = %+v", record)
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
	if _, err := newFolderControlStore(path, nil, nil); err == nil {
		t.Fatal("symlink state was accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema":1,"folders":{"../escape":{"manual":true,"first_sync":true,"pending_rescan":false}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newFolderControlStore(path, nil, nil); err == nil {
		t.Fatal("unsafe folder id was accepted")
	}
}

func TestFolderControlStateMigratesLegacyBindingAndKeepsExternalID(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, folderControlStateName)
	card := cards.Card{Identity: cards.Identity{Version: 1, ID: "00112233445566778899aabbccddeeff"}}
	legacyID, markerName, err := cards.BindingNames(card.Identity.ID, "saves")
	if err != nil {
		t.Fatal(err)
	}
	legacy := `{"schema":1,"folders":{"` + legacyID + `":{"manual":false,"first_sync":true,"first_sync_epoch":1,"pending_rescan":false}}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	folder := syncthing.ConfiguredFolder{ID: legacyID, Kind: "saves", MarkerName: markerName}
	store, err := newFolderControlStore(path, []syncthing.ConfiguredFolder{folder}, []cards.Card{card})
	if err != nil {
		t.Fatal(err)
	}
	record := store.Snapshot()[legacyID]
	if record.CardID != card.Identity.ID || record.Kind != "saves" || record.MarkerName != markerName {
		t.Fatalf("migrated binding = %+v", record)
	}

	external := syncthing.ConfiguredFolder{ID: "retro-saves", Kind: "saves", MarkerName: markerName}
	if err := store.Remove(legacyID); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(external, card); err != nil {
		t.Fatal(err)
	}
	reloaded, err := newFolderControlStore(path, nil, []cards.Card{card})
	if err != nil {
		t.Fatal(err)
	}
	if kinds := reloaded.BindingKinds(); len(kinds) != 1 || kinds[external.ID] != "saves" {
		t.Fatalf("external binding kinds = %#v", kinds)
	}
}

func TestFolderControlStateAllowsSavesAndStatesOnOneCard(t *testing.T) {
	card := cards.Card{Identity: cards.Identity{Version: 1, ID: "00112233445566778899aabbccddeeff"}}
	store, err := newFolderControlStore(filepath.Join(t.TempDir(), folderControlStateName), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"saves", "states"} {
		_, markerName, err := cards.BindingNames(card.Identity.ID, kind)
		if err != nil {
			t.Fatal(err)
		}
		folder := syncthing.ConfiguredFolder{ID: "retro-" + kind, Kind: kind, MarkerName: markerName}
		if err := store.Add(folder, card); err != nil {
			t.Fatalf("add %s binding: %v", kind, err)
		}
	}
	if got := store.Snapshot(); len(got) != 2 {
		t.Fatalf("bindings = %#v", got)
	}
	_, savesMarker, err := cards.BindingNames(card.Identity.ID, "saves")
	if err != nil {
		t.Fatal(err)
	}
	duplicate := syncthing.ConfiguredFolder{ID: "other-saves", Kind: "saves", MarkerName: savesMarker}
	if err := store.Add(duplicate, card); err == nil {
		t.Fatal("second Saves binding for the same card was accepted")
	}
}

func TestFolderControlStatePersistsIgnoredOffers(t *testing.T) {
	path := filepath.Join(t.TempDir(), folderControlStateName)
	store, err := newFolderControlStore(path, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetOfferIgnored("retro-saves", onboardingPeer, true); err != nil {
		t.Fatal(err)
	}
	reloaded, err := newFolderControlStore(path, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := folderOfferKey("retro-saves", onboardingPeer)
	if !reloaded.IgnoredOffers()[key] {
		t.Fatalf("ignored offers = %#v", reloaded.IgnoredOffers())
	}
	if err := reloaded.SetOfferIgnored("retro-saves", onboardingPeer, false); err != nil {
		t.Fatal(err)
	}
	if len(reloaded.IgnoredOffers()) != 0 {
		t.Fatalf("restored offers = %#v", reloaded.IgnoredOffers())
	}
}
