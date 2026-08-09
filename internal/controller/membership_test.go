package controller

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/cards"
	syncthingconfig "github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
)

func TestFolderWithMembershipAddsAndRemovesExactPeer(t *testing.T) {
	fixture := newFirstSyncFixture(t, "sendreceive")
	fixture.folder.Devices = []string{onboardingSelf, onboardingPeer}

	shared, changed, err := folderWithMembership(fixture.folder, onboardingSelf, onboardingOtherPeer, true)
	if err != nil || !changed || !shared.Paused || len(shared.Devices) != 3 || shared.Devices[2] != onboardingPeer {
		t.Fatalf("share = %+v, changed=%v, err=%v", shared, changed, err)
	}
	unshared, changed, err := folderWithMembership(shared, onboardingSelf, onboardingPeer, false)
	if err != nil || !changed || len(unshared.Devices) != 2 || unshared.Devices[1] != onboardingOtherPeer {
		t.Fatalf("unshare = %+v, changed=%v, err=%v", unshared, changed, err)
	}
	localOnly, changed, err := folderWithMembership(fixture.folder, onboardingSelf, onboardingPeer, false)
	if err != nil || !changed || len(localOnly.Devices) != 1 || localOnly.Devices[0] != onboardingSelf {
		t.Fatalf("unshare final peer = %+v, changed=%v, err=%v", localOnly, changed, err)
	}
}

func TestRecoverFolderMembershipCompletesDurableIntent(t *testing.T) {
	fixture := newFirstSyncFixture(t, "sendreceive")
	fixture.folder.Devices = []string{onboardingSelf, onboardingPeer}
	controls, err := newFolderControlStore(
		filepath.Join(t.TempDir(), folderControlStateName),
		[]syncthingconfig.ConfiguredFolder{fixture.folder}, []cards.Card{fixture.card},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := controls.BeginMembership(fixture.folder.ID, onboardingOtherPeer, "share"); err != nil {
		t.Fatal(err)
	}
	upstream := newFakeB3Upstream()
	upstream.folders[fixture.folder.ID] = fixture.folder
	recovered, err := recoverFolderMemberships(
		context.Background(), []syncthingconfig.ConfiguredFolder{fixture.folder}, onboardingSelf,
		[]cards.Card{fixture.card}, controls, upstream,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || len(recovered[0].Devices) != 3 || recovered[0].Devices[1] != onboardingOtherPeer ||
		controls.Snapshot()[fixture.folder.ID].PendingMembership != "" {
		t.Fatalf("recovered folders = %+v, controls=%+v", recovered, controls.Snapshot())
	}
}
