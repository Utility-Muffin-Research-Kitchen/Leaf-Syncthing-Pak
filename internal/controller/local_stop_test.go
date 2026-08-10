package controller

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/cards"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
)

func TestRecoverPendingFolderStopRetainsLiveTree(t *testing.T) {
	fixture := newFirstSyncFixture(t, "sendreceive")
	upstream := newFakeB3Upstream()
	upstream.folders[fixture.folder.ID] = fixture.folder
	if err := fixture.controls.BeginStop(fixture.folder.ID); err != nil {
		t.Fatal(err)
	}
	folders, err := recoverPendingFolderStops(
		context.Background(), []syncthing.ConfiguredFolder{fixture.folder},
		[]cards.Card{fixture.card}, fixture.controls, upstream,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 0 || len(fixture.controls.Snapshot()) != 0 {
		t.Fatalf("stopped folders = %v, controls=%#v", folders, fixture.controls.Snapshot())
	}
	if payload, err := os.ReadFile(filepath.Join(fixture.folder.Path, "slot.sav")); err != nil || string(payload) != "save-data" {
		t.Fatalf("live save = %q, %v", payload, err)
	}
	if _, err := os.Lstat(filepath.Join(fixture.folder.Path, fixture.folder.MarkerName)); !os.IsNotExist(err) {
		t.Fatalf("Leaf marker survived local stop: %v", err)
	}
}

func TestRecoverPendingFolderStopFinishesAfterUpstreamRemoval(t *testing.T) {
	fixture := newFirstSyncFixture(t, "sendonly")
	if err := fixture.controls.BeginStop(fixture.folder.ID); err != nil {
		t.Fatal(err)
	}
	upstream := newFakeB3Upstream()
	folders, err := recoverPendingFolderStops(
		context.Background(), nil, []cards.Card{fixture.card}, fixture.controls, upstream,
	)
	if err != nil || len(folders) != 0 || len(fixture.controls.Snapshot()) != 0 {
		t.Fatalf("recovered local stop = %v, folders=%v controls=%#v", err, folders, fixture.controls.Snapshot())
	}
	if _, err := os.Stat(filepath.Join(fixture.folder.Path, "nested", "meta.bin")); err != nil {
		t.Fatalf("live nested file was not retained: %v", err)
	}
}

func TestRecoverPendingFolderStopRefusesNonemptyMarkerBeforeUpstreamChange(t *testing.T) {
	fixture := newFirstSyncFixture(t, "sendonly")
	if err := os.WriteFile(filepath.Join(fixture.folder.Path, fixture.folder.MarkerName, "unexpected"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fixture.controls.BeginStop(fixture.folder.ID); err != nil {
		t.Fatal(err)
	}
	upstream := newFakeB3Upstream()
	upstream.folders[fixture.folder.ID] = fixture.folder
	if _, err := recoverPendingFolderStops(
		context.Background(), []syncthing.ConfiguredFolder{fixture.folder},
		[]cards.Card{fixture.card}, fixture.controls, upstream,
	); err == nil {
		t.Fatal("nonempty marker was removed")
	}
	if _, present := upstream.folders[fixture.folder.ID]; !present {
		t.Fatal("unsafe stop reached upstream removal")
	}
}
