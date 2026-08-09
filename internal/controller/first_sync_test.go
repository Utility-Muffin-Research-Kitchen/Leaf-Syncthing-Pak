package controller

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/cards"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/leaf"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
)

type firstSyncFixture struct {
	card        cards.Card
	folder      syncthing.ConfiguredFolder
	controls    *folderControlStore
	controlPath string
}

func newFirstSyncFixture(t *testing.T, folderType string) firstSyncFixture {
	t.Helper()
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
	if err := os.Mkdir(filepath.Join(saves, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(saves, "slot.sav"), []byte("save-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(saves, "nested", "meta.bin"), []byte{0, 1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	source := leaf.Source{
		ID: "primary", Root: root, Primary: true,
		UserdataPath: filepath.Join(root, ".userdata", "mlp1"),
		SavesPath:    saves,
		StatesPath:   filepath.Join(root, "States"),
	}
	card := cards.Card{
		Source: source, Identity: cards.Identity{Version: 1, ID: cardID},
		State: cards.StateEnrolled, Present: true, Writable: true,
	}
	folder := syncthing.ConfiguredFolder{
		ID: folderID, Label: "Leaf Saves", Kind: "saves", Path: saves,
		Type: folderType, MarkerName: marker, Paused: true,
	}
	if folderType != "sendonly" {
		folder.VersioningType = "simple"
		folder.VersioningFSType = "basic"
		folder.VersioningFSPath = filepath.Join(source.UserdataPath, leaf.AppStateName, "versions", "saves")
	}
	controlPath := filepath.Join(t.TempDir(), folderControlStateName)
	controls, err := newFolderControlStore(controlPath, []syncthing.ConfiguredFolder{folder})
	if err != nil {
		t.Fatal(err)
	}
	return firstSyncFixture{card: card, folder: folder, controls: controls, controlPath: controlPath}
}

func fixedFirstSyncOptions(syncCalls *[]string, faultAt string) firstSyncOptions {
	return firstSyncOptions{
		Now:    func() time.Time { return time.Date(2026, time.August, 9, 12, 34, 56, 0, time.UTC) },
		Random: bytes.NewReader([]byte{0xde, 0xad, 0xbe, 0xef}),
		SyncFilesystem: func(path string) error {
			if syncCalls != nil {
				*syncCalls = append(*syncCalls, path)
			}
			return nil
		},
		RequireFreeSpace: func(string, int64) error { return nil },
		Fault: func(point string) error {
			if point == faultAt {
				return errors.New("injected crash")
			}
			return nil
		},
	}
}

func TestFirstSyncPrepareCopiesHashedSnapshotAndCompletesDurably(t *testing.T) {
	fixture := newFirstSyncFixture(t, "sendreceive")
	syncCalls := []string{}
	manager, err := newFirstSyncManager([]syncthing.ConfiguredFolder{fixture.folder}, []cards.Card{fixture.card}, fixture.controls, fixedFirstSyncOptions(&syncCalls, ""))
	if err != nil {
		t.Fatal(err)
	}
	progress, err := manager.Prepare(context.Background(), fixture.folder, fixture.card, fixture.controls)
	if err != nil {
		t.Fatal(err)
	}
	if progress.State != "ready" || progress.FileCount != 2 || progress.DirectoryCount != 1 || progress.ContentBytes != 13 {
		t.Fatalf("progress = %+v", progress)
	}
	if len(syncCalls) != 3 {
		t.Fatalf("snapshot syncfs calls = %d, want 3", len(syncCalls))
	}

	snapshotRoot := filepath.Join(firstSyncKindRoot(fixture.card, "saves"), progress.SnapshotName)
	epoch := fixture.controls.Snapshot()[fixture.folder.ID].FirstSyncEpoch
	header, ok, err := readSnapshotHeader(snapshotRoot, fixture.folder, fixture.card, epoch)
	if err != nil || !ok || header.State != "ready" || header.SourceRelative != "Saves" {
		t.Fatalf("header = %+v, ok=%v, err=%v", header, ok, err)
	}
	entries := readSnapshotManifestForTest(t, filepath.Join(snapshotRoot, snapshotManifestName))
	if len(entries) != 3 {
		t.Fatalf("manifest entries = %+v", entries)
	}
	wantHashes := map[string]string{
		"slot.sav":        hashString("save-data"),
		"nested/meta.bin": hashBytes([]byte{0, 1, 2, 3}),
	}
	for _, entry := range entries {
		if entry.Type == "file" && entry.SHA256 != wantHashes[entry.Path] {
			t.Fatalf("manifest hash for %s = %q", entry.Path, entry.SHA256)
		}
		if strings.Contains(entry.Path, fixture.folder.MarkerName) {
			t.Fatalf("managed marker leaked into snapshot: %+v", entry)
		}
	}
	if _, err := os.Lstat(filepath.Join(snapshotRoot, snapshotFilesName, fixture.folder.MarkerName)); !os.IsNotExist(err) {
		t.Fatalf("managed marker snapshot = %v", err)
	}

	if err := manager.Complete(fixture.folder, fixture.card, fixture.controls, false); err == nil {
		t.Fatal("completion without hub acknowledgment succeeded")
	}
	if !fixture.controls.Snapshot()[fixture.folder.ID].FirstSync {
		t.Fatal("failed completion cleared first-sync")
	}
	if err := manager.Complete(fixture.folder, fixture.card, fixture.controls, true); err != nil {
		t.Fatal(err)
	}
	if fixture.controls.Snapshot()[fixture.folder.ID].FirstSync {
		t.Fatal("durable completion did not clear first-sync")
	}
	if len(syncCalls) != 6 { // three prepare barriers, snapshot confirmation, two marker barriers
		t.Fatalf("total syncfs calls = %d, want 6", len(syncCalls))
	}
	marker, ok, err := readFirstSyncMarker(fixture.card, fixture.folder, epoch)
	if err != nil || !ok || marker.State != "complete" || marker.Mode != "snapshot" || marker.SnapshotName != progress.SnapshotName {
		t.Fatalf("completion marker = %+v, ok=%v, err=%v", marker, ok, err)
	}

	reloadedControls, err := newFolderControlStore(fixture.controlPath, []syncthing.ConfiguredFolder{fixture.folder})
	if err != nil {
		t.Fatal(err)
	}
	recoverySyncs := []string{}
	reloaded, err := newFirstSyncManager([]syncthing.ConfiguredFolder{fixture.folder}, []cards.Card{fixture.card}, reloadedControls, fixedFirstSyncOptions(&recoverySyncs, ""))
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Progress(fixture.folder.ID, reloadedControls.Snapshot()[fixture.folder.ID].FirstSync); got.State != "complete" {
		t.Fatalf("reloaded progress = %+v", got)
	}
	if len(recoverySyncs) != 0 {
		t.Fatalf("already committed recovery repeated syncfs: %v", recoverySyncs)
	}
}

func TestFirstSyncPrepareRefusesUnsafeSourcesAndSpaceFailure(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(t *testing.T, fixture firstSyncFixture)
		space error
		want  string
	}{
		{
			name: "foreign marker",
			setup: func(t *testing.T, fixture firstSyncFixture) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(fixture.folder.Path, ".stfolder"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			want: "foreign",
		},
		{
			name: "symlink",
			setup: func(t *testing.T, fixture firstSyncFixture) {
				t.Helper()
				if err := os.Symlink(filepath.Join(fixture.folder.Path, "slot.sav"), filepath.Join(fixture.folder.Path, "link")); err != nil {
					t.Fatal(err)
				}
			},
			want: "symlink",
		},
		{name: "no space", space: errors.New("ENOSPC"), want: "ENOSPC"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFirstSyncFixture(t, "sendreceive")
			if test.setup != nil {
				test.setup(t, fixture)
			}
			options := fixedFirstSyncOptions(nil, "")
			if test.space != nil {
				options.RequireFreeSpace = func(string, int64) error { return test.space }
			}
			manager, err := newFirstSyncManager([]syncthing.ConfiguredFolder{fixture.folder}, []cards.Card{fixture.card}, fixture.controls, options)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Prepare(context.Background(), fixture.folder, fixture.card, fixture.controls); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("prepare error = %v, want %q", err, test.want)
			}
			if !fixture.controls.Snapshot()[fixture.folder.ID].FirstSync {
				t.Fatal("failed prepare cleared first-sync")
			}
		})
	}
}

func TestFirstSyncSnapshotCrashRecoveryMatrix(t *testing.T) {
	for _, point := range []string{
		"snapshot-copied", "snapshot-synced", "snapshot-ready", "snapshot-promoted", "snapshot-committed",
	} {
		t.Run(point, func(t *testing.T) {
			fixture := newFirstSyncFixture(t, "sendreceive")
			manager, err := newFirstSyncManager([]syncthing.ConfiguredFolder{fixture.folder}, []cards.Card{fixture.card}, fixture.controls, fixedFirstSyncOptions(nil, point))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Prepare(context.Background(), fixture.folder, fixture.card, fixture.controls); err == nil {
				t.Fatal("fault did not interrupt prepare")
			}
			reloadedControls, err := newFolderControlStore(fixture.controlPath, []syncthing.ConfiguredFolder{fixture.folder})
			if err != nil {
				t.Fatal(err)
			}
			reloaded, err := newFirstSyncManager([]syncthing.ConfiguredFolder{fixture.folder}, []cards.Card{fixture.card}, reloadedControls, fixedFirstSyncOptions(nil, ""))
			if err != nil {
				t.Fatal(err)
			}
			progress := reloaded.Progress(fixture.folder.ID, reloadedControls.Snapshot()[fixture.folder.ID].FirstSync)
			want := "required"
			if point == "snapshot-promoted" || point == "snapshot-committed" {
				want = "ready"
			}
			if progress.State != want || !reloadedControls.Snapshot()[fixture.folder.ID].FirstSync {
				t.Fatalf("recovered progress = %+v, control=%+v, want %s and protected", progress, reloadedControls.Snapshot(), want)
			}
			assertNoPartialSnapshots(t, firstSyncKindRoot(fixture.card, "saves"))
		})
	}
}

func TestFirstSyncCompletionCrashRecoveryMatrix(t *testing.T) {
	for _, point := range []string{
		"completion-pending-written", "completion-pending-synced", "completion-ready-written",
		"completion-ready-synced", "control-cleared",
	} {
		t.Run(point, func(t *testing.T) {
			fixture := newFirstSyncFixture(t, "sendreceive")
			manager, err := newFirstSyncManager([]syncthing.ConfiguredFolder{fixture.folder}, []cards.Card{fixture.card}, fixture.controls, fixedFirstSyncOptions(nil, ""))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Prepare(context.Background(), fixture.folder, fixture.card, fixture.controls); err != nil {
				t.Fatal(err)
			}
			manager.options.Fault = fixedFirstSyncOptions(nil, point).Fault
			if err := manager.Complete(fixture.folder, fixture.card, fixture.controls, true); err == nil {
				t.Fatal("fault did not interrupt completion")
			}

			reloadedControls, err := newFolderControlStore(fixture.controlPath, []syncthing.ConfiguredFolder{fixture.folder})
			if err != nil {
				t.Fatal(err)
			}
			recoverySyncs := []string{}
			reloaded, err := newFirstSyncManager([]syncthing.ConfiguredFolder{fixture.folder}, []cards.Card{fixture.card}, reloadedControls, fixedFirstSyncOptions(&recoverySyncs, ""))
			if err != nil {
				t.Fatal(err)
			}
			control := reloadedControls.Snapshot()[fixture.folder.ID]
			progress := reloaded.Progress(fixture.folder.ID, control.FirstSync)
			switch point {
			case "completion-pending-written", "completion-pending-synced":
				if !control.FirstSync || progress.State != "ready" || len(recoverySyncs) != 0 {
					t.Fatalf("pending recovery = control %+v progress %+v syncs %v", control, progress, recoverySyncs)
				}
			default:
				if control.FirstSync || progress.State != "complete" {
					t.Fatalf("complete recovery = control %+v progress %+v", control, progress)
				}
				wantSyncs := 1
				if point == "control-cleared" {
					wantSyncs = 0
				}
				if len(recoverySyncs) != wantSyncs {
					t.Fatalf("recovery syncfs calls = %d, want %d", len(recoverySyncs), wantSyncs)
				}
			}
		})
	}
}

func TestFirstSyncSendOnlyCompletionAndReceiveTransition(t *testing.T) {
	fixture := newFirstSyncFixture(t, "sendonly")
	manager, err := newFirstSyncManager([]syncthing.ConfiguredFolder{fixture.folder}, []cards.Card{fixture.card}, fixture.controls, fixedFirstSyncOptions(nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Prepare(context.Background(), fixture.folder, fixture.card, fixture.controls); err == nil {
		t.Fatal("send-only prepare unexpectedly created a snapshot")
	}
	if err := manager.Complete(fixture.folder, fixture.card, fixture.controls, true); err != nil {
		t.Fatal(err)
	}
	epoch := fixture.controls.Snapshot()[fixture.folder.ID].FirstSyncEpoch
	marker, ok, err := readFirstSyncMarker(fixture.card, fixture.folder, epoch)
	if err != nil || !ok || marker.Mode != "sendonly" || marker.SnapshotName != "" {
		t.Fatalf("send-only marker = %+v, ok=%v, err=%v", marker, ok, err)
	}

	receive := fixture.folder
	receive.Type = "sendreceive"
	receive.VersioningType = "simple"
	receive.VersioningFSType = "basic"
	receive.VersioningFSPath = filepath.Join(fixture.card.Source.UserdataPath, leaf.AppStateName, "versions", "saves")
	if _, ok, err := readFirstSyncMarker(fixture.card, receive, epoch); err != nil || ok {
		t.Fatalf("send-only marker accepted after receive transition: ok=%v err=%v", ok, err)
	}
	if err := manager.Invalidate(fixture.folder, fixture.card, fixture.controls); err != nil {
		t.Fatal(err)
	}
	if !fixture.controls.Snapshot()[fixture.folder.ID].FirstSync {
		t.Fatal("receive transition did not restore first-sync protection")
	}
	newEpoch := fixture.controls.Snapshot()[fixture.folder.ID].FirstSyncEpoch
	if _, present, err := readFirstSyncMarker(fixture.card, receive, newEpoch); err != nil || present {
		t.Fatalf("completion marker survived invalidation: present=%v err=%v", present, err)
	}
}

func TestFirstSyncEpochPreventsOldSnapshotReuseAfterSendOnly(t *testing.T) {
	fixture := newFirstSyncFixture(t, "sendreceive")
	manager, err := newFirstSyncManager([]syncthing.ConfiguredFolder{fixture.folder}, []cards.Card{fixture.card}, fixture.controls, fixedFirstSyncOptions(nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Prepare(context.Background(), fixture.folder, fixture.card, fixture.controls); err != nil {
		t.Fatal(err)
	}
	if err := manager.Complete(fixture.folder, fixture.card, fixture.controls, true); err != nil {
		t.Fatal(err)
	}
	firstEpoch := fixture.controls.Snapshot()[fixture.folder.ID].FirstSyncEpoch
	if err := manager.Invalidate(fixture.folder, fixture.card, fixture.controls); err != nil {
		t.Fatal(err)
	}
	sendOnly := fixture.folder
	sendOnly.Type = "sendonly"
	if err := manager.Complete(sendOnly, fixture.card, fixture.controls, true); err != nil {
		t.Fatal(err)
	}
	if err := manager.Invalidate(sendOnly, fixture.card, fixture.controls); err != nil {
		t.Fatal(err)
	}
	thirdEpoch := fixture.controls.Snapshot()[fixture.folder.ID].FirstSyncEpoch
	if thirdEpoch != firstEpoch+2 {
		t.Fatalf("transition epoch = %d, want %d", thirdEpoch, firstEpoch+2)
	}
	if err := manager.Complete(fixture.folder, fixture.card, fixture.controls, true); err == nil || !strings.Contains(err.Error(), "safety snapshot") {
		t.Fatalf("old snapshot satisfied new receive epoch: %v", err)
	}
	manager.options.Random = bytes.NewReader([]byte{0xca, 0xfe, 0xba, 0xbe})
	if _, err := manager.Prepare(context.Background(), fixture.folder, fixture.card, fixture.controls); err != nil {
		t.Fatal(err)
	}
	if err := manager.Complete(fixture.folder, fixture.card, fixture.controls, true); err != nil {
		t.Fatal(err)
	}
}

func TestFirstSyncRecoveryRefusesToClearBeforeRecoverySyncfs(t *testing.T) {
	fixture := newFirstSyncFixture(t, "sendonly")
	manager, err := newFirstSyncManager([]syncthing.ConfiguredFolder{fixture.folder}, []cards.Card{fixture.card}, fixture.controls, fixedFirstSyncOptions(nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	manager.options.Fault = fixedFirstSyncOptions(nil, "completion-ready-written").Fault
	if err := manager.Complete(fixture.folder, fixture.card, fixture.controls, true); err == nil {
		t.Fatal("completion fault did not fire")
	}
	reloadedControls, err := newFolderControlStore(fixture.controlPath, []syncthing.ConfiguredFolder{fixture.folder})
	if err != nil {
		t.Fatal(err)
	}
	options := fixedFirstSyncOptions(nil, "")
	options.SyncFilesystem = func(string) error { return errors.New("flush failed") }
	if _, err := newFirstSyncManager([]syncthing.ConfiguredFolder{fixture.folder}, []cards.Card{fixture.card}, reloadedControls, options); err == nil || !strings.Contains(err.Error(), "flush failed") {
		t.Fatalf("recovery error = %v", err)
	}
	if !reloadedControls.Snapshot()[fixture.folder.ID].FirstSync {
		t.Fatal("failed recovery barrier cleared first-sync")
	}
}

func TestFirstSyncRecoveryDoesNotRewriteCompletionWhileCardUnavailable(t *testing.T) {
	fixture := newFirstSyncFixture(t, "sendonly")
	if err := fixture.controls.SetFirstSync(fixture.folder.ID, false); err != nil {
		t.Fatal(err)
	}
	unavailable := fixture.card
	unavailable.Present = false
	unavailable.Writable = false
	unavailable.State = cards.StateAbsent
	if _, err := newFirstSyncManager([]syncthing.ConfiguredFolder{fixture.folder}, []cards.Card{unavailable}, fixture.controls, fixedFirstSyncOptions(nil, "")); err != nil {
		t.Fatal(err)
	}
	if fixture.controls.Snapshot()[fixture.folder.ID].FirstSync {
		t.Fatal("card absence rewrote a previously completed first-sync decision")
	}
	if _, err := newFirstSyncManager([]syncthing.ConfiguredFolder{fixture.folder}, []cards.Card{fixture.card}, fixture.controls, fixedFirstSyncOptions(nil, "")); err != nil {
		t.Fatal(err)
	}
	if !fixture.controls.Snapshot()[fixture.folder.ID].FirstSync {
		t.Fatal("present card without its completion marker remained unprotected")
	}
}

func readSnapshotManifestForTest(t *testing.T, path string) []snapshotManifestEntry {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	entries := []snapshotManifestEntry{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var entry snapshotManifestEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return entries
}

func hashString(value string) string { return hashBytes([]byte(value)) }

func hashBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func assertNoPartialSnapshots(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), snapshotPartialPrefix) || entry.Name() == firstSyncMarkerTemporary {
			t.Fatalf("partial first-sync artifact survived recovery: %s", entry.Name())
		}
	}
}
