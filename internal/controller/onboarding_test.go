package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/cards"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
)

const (
	onboardingSelf      = "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"
	onboardingPeer      = "IIIIIII-JJJJJJJ-KKKKKKK-LLLLLLL-MMMMMMM-NNNNNNN-OOOOOOO-PPPPPPP"
	onboardingOtherPeer = "CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH-IIIIIII-JJJJJJJ"
)

type fakeManagedFolderUpstream struct {
	devices []string
	added   []syncthing.ConfiguredFolder
	addErr  error
}

func (upstream *fakeManagedFolderUpstream) ConfiguredFolderDevices(context.Context, string) ([]string, error) {
	return append([]string(nil), upstream.devices...), nil
}

func (upstream *fakeManagedFolderUpstream) AddManagedFolder(_ context.Context, folder syncthing.ConfiguredFolder) error {
	if upstream.addErr != nil {
		return upstream.addErr
	}
	upstream.added = append(upstream.added, folder)
	return nil
}

func TestFolderOnboardingPlanAndCreateConfinedPausedFolder(t *testing.T) {
	fixture := newFirstSyncFixture(t, "sendreceive")
	if err := os.Remove(filepath.Join(fixture.folder.Path, fixture.folder.MarkerName)); err != nil {
		t.Fatal(err)
	}
	controlPath := filepath.Join(t.TempDir(), folderControlStateName)
	controls, err := newFolderControlStore(controlPath, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	upstream := &fakeManagedFolderUpstream{devices: []string{onboardingSelf, onboardingPeer, onboardingOtherPeer}}
	syncCalls := []string{}
	manager := newOnboardingManager(onboardingOptions{
		Now:            func() time.Time { return time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC) },
		Random:         strings.NewReader(strings.Repeat("a", 16)),
		AvailableBytes: func(string) (uint64, error) { return 64 * 1024 * 1024, nil },
		SyncFilesystem: func(path string) error {
			syncCalls = append(syncCalls, path)
			return nil
		},
	})
	plan, err := manager.Plan(context.Background(), "primary", "saves", "sendreceive", onboardingSelf, []string{onboardingPeer}, []cards.Card{fixture.card}, nil, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ID) != 32 || plan.CardID != fixture.card.Identity.ID || plan.FolderID != fixture.folder.ID ||
		plan.FileCount != 2 || plan.DirectoryCount != 1 || plan.ContentBytes != 13 ||
		!plan.SnapshotPossible || plan.PeerCount != 1 || plan.StatesWarning {
		t.Fatalf("onboarding plan = %+v", plan)
	}
	folder, err := manager.Create(context.Background(), plan.ID, onboardingSelf, false, true, []cards.Card{fixture.card}, nil, controls, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if len(upstream.added) != 1 || !folder.Paused || folder.Type != "sendreceive" ||
		folder.VersioningType != "simple" || folder.VersioningFSType != "basic" || len(folder.Devices) != 2 ||
		folder.Devices[0] != onboardingSelf || folder.Devices[1] != onboardingPeer {
		t.Fatalf("created folder = %+v, API=%+v", folder, upstream.added)
	}
	if !controls.Snapshot()[folder.ID].FirstSync || controls.Snapshot()[folder.ID].PendingAdd {
		t.Fatalf("created folder control = %+v", controls.Snapshot())
	}
	if err := cards.ValidateManagedMarker(folder.Path, folder.MarkerName); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(folder.VersioningFSPath); err != nil || !info.IsDir() {
		t.Fatalf("versioning directory = %v, err=%v", info, err)
	}
	if len(syncCalls) != 1 || syncCalls[0] != fixture.card.Source.Root {
		t.Fatalf("storage syncfs calls = %v", syncCalls)
	}
	if _, err := manager.Create(context.Background(), plan.ID, onboardingSelf, false, true, []cards.Card{fixture.card}, nil, controls, upstream); err == nil {
		t.Fatal("consumed onboarding plan was reused")
	}
}

func TestFolderOfferPlanKeepsNetworkIDAndOnlyOfferingPeer(t *testing.T) {
	fixture := newFirstSyncFixture(t, "sendreceive")
	if err := os.Remove(filepath.Join(fixture.folder.Path, fixture.folder.MarkerName)); err != nil {
		t.Fatal(err)
	}
	controls, err := newFolderControlStore(filepath.Join(t.TempDir(), folderControlStateName), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	upstream := &fakeManagedFolderUpstream{devices: []string{onboardingSelf, onboardingPeer, onboardingOtherPeer}}
	manager := newOnboardingManager(onboardingOptions{
		Random:         strings.NewReader(strings.Repeat("h", 16)),
		AvailableBytes: func(string) (uint64, error) { return 64 * 1024 * 1024, nil },
		SyncFilesystem: func(string) error { return nil },
	})
	plan, err := manager.PlanOffer(
		context.Background(), "primary", "saves", "sendreceive", "retro-saves", "Retro Saves",
		onboardingPeer, onboardingSelf, []cards.Card{fixture.card}, nil, upstream,
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.FolderID != "retro-saves" || plan.Label != "Retro Saves" || plan.OfferDeviceID != onboardingPeer || plan.PeerCount != 1 {
		t.Fatalf("offer plan = %+v", plan)
	}
	folder, err := manager.Create(context.Background(), plan.ID, onboardingSelf, false, true, []cards.Card{fixture.card}, nil, controls, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if folder.ID != "retro-saves" || len(folder.Devices) != 2 || folder.Devices[0] != onboardingSelf || folder.Devices[1] != onboardingPeer {
		t.Fatalf("accepted offer folder = %+v", folder)
	}
	if record := controls.Snapshot()[folder.ID]; record.CardID != fixture.card.Identity.ID || record.Kind != "saves" || record.PendingAdd {
		t.Fatalf("accepted offer binding = %+v", record)
	}
}

func TestSelectedFolderDevicesRequireExplicitConfiguredPeers(t *testing.T) {
	upstream := &fakeManagedFolderUpstream{devices: []string{onboardingSelf, onboardingPeer, onboardingOtherPeer}}
	devices, err := selectedFolderDevices(context.Background(), onboardingSelf, []string{onboardingPeer}, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 || devices[0] != onboardingSelf || devices[1] != onboardingPeer {
		t.Fatalf("selected devices = %v", devices)
	}
	for _, selected := range [][]string{
		nil,
		{onboardingPeer, onboardingPeer},
		{"QQQQQQQ-RRRRRRR-SSSSSSS-TTTTTTT-UUUUUUU-VVVVVVV-WWWWWWW-XXXXXXX"},
	} {
		if _, err := selectedFolderDevices(context.Background(), onboardingSelf, selected, upstream); err == nil {
			t.Fatalf("unsafe peer selection accepted: %v", selected)
		}
	}
}

func TestFolderOnboardingRefusesForeignManagerAndDuplicateBinding(t *testing.T) {
	fixture := newFirstSyncFixture(t, "sendreceive")
	upstream := &fakeManagedFolderUpstream{devices: []string{onboardingSelf, onboardingPeer}}
	manager := newOnboardingManager(onboardingOptions{
		Random:         strings.NewReader(strings.Repeat("b", 32)),
		AvailableBytes: func(string) (uint64, error) { return 64 * 1024 * 1024, nil },
	})
	if err := os.Mkdir(filepath.Join(fixture.folder.Path, ".stfolder"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Plan(context.Background(), "primary", "saves", "sendreceive", onboardingSelf, []string{onboardingPeer}, []cards.Card{fixture.card}, nil, upstream); !errors.Is(err, cards.ErrForeignMarker) {
		t.Fatalf("foreign marker error = %v", err)
	}
	if err := os.Remove(filepath.Join(fixture.folder.Path, ".stfolder")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Plan(context.Background(), "primary", "saves", "sendreceive", onboardingSelf, []string{onboardingPeer}, []cards.Card{fixture.card}, []syncthing.ConfiguredFolder{fixture.folder}, upstream); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("duplicate folder error = %v", err)
	}
}

func TestFolderOnboardingRefusesReceiveFolderWithoutCurrentSnapshotSpace(t *testing.T) {
	fixture := newFirstSyncFixture(t, "sendreceive")
	if err := os.Remove(filepath.Join(fixture.folder.Path, fixture.folder.MarkerName)); err != nil {
		t.Fatal(err)
	}
	controls, err := newFolderControlStore(filepath.Join(t.TempDir(), folderControlStateName), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	upstream := &fakeManagedFolderUpstream{devices: []string{onboardingSelf, onboardingPeer}}
	manager := newOnboardingManager(onboardingOptions{
		Random:         strings.NewReader(strings.Repeat("c", 32)),
		AvailableBytes: func(string) (uint64, error) { return 1, nil },
		SyncFilesystem: func(string) error { return nil },
	})
	plan, err := manager.Plan(context.Background(), "primary", "saves", "sendreceive", onboardingSelf, []string{onboardingPeer}, []cards.Card{fixture.card}, nil, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if plan.SnapshotPossible {
		t.Fatalf("one available byte reported snapshot possible: %+v", plan)
	}
	if _, err := manager.Create(context.Background(), plan.ID, onboardingSelf, false, true, []cards.Card{fixture.card}, nil, controls, upstream); err == nil || !strings.Contains(err.Error(), "insufficient free space") {
		t.Fatalf("receive create without snapshot space error = %v", err)
	}
	if _, exists := controls.Snapshot()[fixture.folder.ID]; exists {
		t.Fatalf("space rejection left control record: %+v", controls.Snapshot())
	}
	if len(upstream.added) != 0 {
		t.Fatalf("space rejection reached upstream API: %+v", upstream.added)
	}
	if _, err := os.Lstat(filepath.Join(fixture.folder.Path, fixture.folder.MarkerName)); !os.IsNotExist(err) {
		t.Fatalf("space rejection created managed marker: %v", err)
	}
	sendOnlyPlan, err := manager.Plan(context.Background(), "primary", "saves", "sendonly", onboardingSelf, []string{onboardingPeer}, []cards.Card{fixture.card}, nil, upstream)
	if err != nil {
		t.Fatal(err)
	}
	folder, err := manager.Create(context.Background(), sendOnlyPlan.ID, onboardingSelf, false, true, []cards.Card{fixture.card}, nil, controls, upstream)
	if err != nil {
		t.Fatalf("send-only fallback with no snapshot space: %v", err)
	}
	if folder.Type != "sendonly" || folder.VersioningType != "" || len(upstream.added) != 1 {
		t.Fatalf("send-only fallback = %+v, API=%+v", folder, upstream.added)
	}
}

func TestFolderOnboardingRechecksSnapshotSpaceAtCreate(t *testing.T) {
	fixture := newFirstSyncFixture(t, "sendreceive")
	if err := os.Remove(filepath.Join(fixture.folder.Path, fixture.folder.MarkerName)); err != nil {
		t.Fatal(err)
	}
	controls, err := newFolderControlStore(filepath.Join(t.TempDir(), folderControlStateName), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	available := uint64(64 * 1024 * 1024)
	upstream := &fakeManagedFolderUpstream{devices: []string{onboardingSelf, onboardingPeer}}
	manager := newOnboardingManager(onboardingOptions{
		Random:         strings.NewReader(strings.Repeat("f", 16)),
		AvailableBytes: func(string) (uint64, error) { return available, nil },
		SyncFilesystem: func(string) error { return nil },
	})
	plan, err := manager.Plan(context.Background(), "primary", "saves", "sendreceive", onboardingSelf, []string{onboardingPeer}, []cards.Card{fixture.card}, nil, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.SnapshotPossible {
		t.Fatalf("initial review unexpectedly rejected snapshot: %+v", plan)
	}
	available = 1
	if _, err := manager.Create(context.Background(), plan.ID, onboardingSelf, false, true, []cards.Card{fixture.card}, nil, controls, upstream); err == nil || !strings.Contains(err.Error(), "insufficient free space") {
		t.Fatalf("space-race create error = %v", err)
	}
	if len(upstream.added) != 0 || len(controls.Snapshot()) != 0 {
		t.Fatalf("space-race rejection mutated state: API=%+v controls=%+v", upstream.added, controls.Snapshot())
	}
}

func TestFolderOnboardingRollsBackControlStateAfterAPIFailure(t *testing.T) {
	fixture := newFirstSyncFixture(t, "sendreceive")
	if err := os.Remove(filepath.Join(fixture.folder.Path, fixture.folder.MarkerName)); err != nil {
		t.Fatal(err)
	}
	controls, err := newFolderControlStore(filepath.Join(t.TempDir(), folderControlStateName), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	upstream := &fakeManagedFolderUpstream{
		devices: []string{onboardingSelf, onboardingPeer}, addErr: errors.New("fixture API failure"),
	}
	manager := newOnboardingManager(onboardingOptions{
		Random:         strings.NewReader(strings.Repeat("g", 16)),
		AvailableBytes: func(string) (uint64, error) { return 64 * 1024 * 1024, nil },
		SyncFilesystem: func(string) error { return nil },
	})
	plan, err := manager.Plan(context.Background(), "primary", "saves", "sendreceive", onboardingSelf, []string{onboardingPeer}, []cards.Card{fixture.card}, nil, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(context.Background(), plan.ID, onboardingSelf, false, true, []cards.Card{fixture.card}, nil, controls, upstream); err == nil || !strings.Contains(err.Error(), "fixture API failure") {
		t.Fatalf("upstream API failure = %v", err)
	}
	if _, exists := controls.Snapshot()[fixture.folder.ID]; exists {
		t.Fatalf("failed API left control record: %+v", controls.Snapshot())
	}
}

func TestFolderOnboardingPlanExpiresAndDetectsCardSwap(t *testing.T) {
	fixture := newFirstSyncFixture(t, "sendonly")
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	manager := newOnboardingManager(onboardingOptions{
		Now:            func() time.Time { return now },
		Random:         strings.NewReader(strings.Repeat("d", 16)),
		AvailableBytes: func(string) (uint64, error) { return 64 * 1024 * 1024, nil },
		SyncFilesystem: func(string) error { return nil },
	})
	upstream := &fakeManagedFolderUpstream{devices: []string{onboardingSelf, onboardingPeer}}
	plan, err := manager.Plan(context.Background(), "primary", "saves", "sendonly", onboardingSelf, []string{onboardingPeer}, []cards.Card{fixture.card}, nil, upstream)
	if err != nil {
		t.Fatal(err)
	}
	swapped := fixture.card
	swapped.Identity.ID = "ffeeddccbbaa99887766554433221100"
	controls, err := newFolderControlStore(filepath.Join(t.TempDir(), folderControlStateName), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(context.Background(), plan.ID, onboardingSelf, false, true, []cards.Card{swapped}, nil, controls, upstream); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("card-swap error = %v", err)
	}
	now = now.Add(onboardingPlanLifetime)
	if _, err := manager.Create(context.Background(), plan.ID, onboardingSelf, false, true, []cards.Card{fixture.card}, nil, controls, upstream); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired-plan error = %v", err)
	}
}

func TestStatesOnboardingRequiresItsSpecificWarning(t *testing.T) {
	fixture := newFirstSyncFixture(t, "sendreceive")
	manager := newOnboardingManager(onboardingOptions{
		Random:         strings.NewReader(strings.Repeat("e", 16)),
		AvailableBytes: func(string) (uint64, error) { return 64 * 1024 * 1024, nil },
		SyncFilesystem: func(string) error { return nil },
	})
	upstream := &fakeManagedFolderUpstream{devices: []string{onboardingSelf, onboardingPeer}}
	plan, err := manager.Plan(context.Background(), "primary", "states", "sendonly", onboardingSelf, []string{onboardingPeer}, []cards.Card{fixture.card}, nil, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.StatesWarning {
		t.Fatalf("states plan omitted warning: %+v", plan)
	}
	controls, err := newFolderControlStore(filepath.Join(t.TempDir(), folderControlStateName), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(context.Background(), plan.ID, onboardingSelf, false, true, []cards.Card{fixture.card}, nil, controls, upstream); err == nil || !strings.Contains(err.Error(), "warnings") {
		t.Fatalf("unacknowledged states warning = %v", err)
	}
}
