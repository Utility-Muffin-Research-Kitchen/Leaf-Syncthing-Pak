package controller

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/uicontrol"
)

func TestApplyLiveStatusIncludesFolderOffers(t *testing.T) {
	deviceID := "IIIIIII-JJJJJJJ-KKKKKKK-LLLLLLL-MMMMMMM-NNNNNNN-OOOOOOO-PPPPPPP"
	status := applyLiveStatus(uicontrol.Status{}, syncthing.UIStatus{
		Folders: map[string]syncthing.UIFolderStatus{},
		FolderOffers: []syncthing.UIFolderOffer{{
			FolderID: "retro-saves", Label: "Retro Saves", DeviceID: deviceID,
			DeviceName: "Laptop", OfferedAt: "2026-08-09T12:34:56Z",
		}},
	})
	if len(status.FolderOffers) != 1 || status.FolderOffers[0].FolderID != "retro-saves" ||
		status.FolderOffers[0].DeviceIDSuffix != "PPPPPPP" || status.FolderOffers[0].DeviceName != "Laptop" {
		t.Fatalf("folder offers = %+v", status.FolderOffers)
	}
}

func TestApplyLiveStatusIncludesLocalAndRemoteNeed(t *testing.T) {
	status := applyLiveStatus(uicontrol.Status{Folders: []uicontrol.FolderStatus{{
		ID: "retro-saves", Issues: []uicontrol.Issue{},
	}}}, syncthing.UIStatus{Folders: map[string]syncthing.UIFolderStatus{
		"retro-saves": {
			ID: "retro-saves", State: "syncing", NeedBytes: 10, NeedItems: 2,
			RemoteState: "syncing", RemotePeer: "Laptop", RemoteBytes: 20, RemoteItems: 3,
		},
	}})
	folder := status.Folders[0]
	if folder.NeedBytes != 10 || folder.NeedItems != 2 || folder.RemoteState != "syncing" ||
		folder.RemotePeer != "Laptop" || folder.RemoteNeedBytes != 20 || folder.RemoteNeedItems != 3 {
		t.Fatalf("folder completion = %+v", folder)
	}
}

func TestRecoverPendingDeviceRemoval(t *testing.T) {
	controls, err := newFolderControlStore(filepath.Join(t.TempDir(), folderControlStateName), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := controls.BeginDeviceRemoval(onboardingPeer); err != nil {
		t.Fatal(err)
	}
	upstream := newFakeB3Upstream()
	if err := recoverPendingDeviceRemoval(context.Background(), onboardingSelf, nil, controls, upstream); err != nil {
		t.Fatal(err)
	}
	if controls.PendingDeviceRemoval() != "" || containsDevice(upstream.devices, onboardingPeer) {
		t.Fatalf("recovered removal = %q, devices=%v", controls.PendingDeviceRemoval(), upstream.devices)
	}
}

func TestRecoverPendingDeviceRemovalRefusesFolderMembership(t *testing.T) {
	controls, err := newFolderControlStore(filepath.Join(t.TempDir(), folderControlStateName), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := controls.BeginDeviceRemoval(onboardingPeer); err != nil {
		t.Fatal(err)
	}
	upstream := newFakeB3Upstream()
	folders := []syncthing.ConfiguredFolder{{Label: "Leaf Saves", Devices: []string{onboardingSelf, onboardingPeer}}}
	if err := recoverPendingDeviceRemoval(context.Background(), onboardingSelf, folders, controls, upstream); err == nil {
		t.Fatal("device removal with a folder membership was recovered")
	}
	if controls.PendingDeviceRemoval() != onboardingPeer || !containsDevice(upstream.devices, onboardingPeer) {
		t.Fatalf("blocked recovery = %q, devices=%v", controls.PendingDeviceRemoval(), upstream.devices)
	}
}

func TestApplyIgnoredFolderOffersMatchesFolderAndDevice(t *testing.T) {
	status := uicontrol.Status{FolderOffers: []uicontrol.FolderOfferStatus{
		{FolderID: "retro-saves", DeviceID: onboardingPeer},
		{FolderID: "retro-states", DeviceID: onboardingPeer},
	}}
	status = applyIgnoredFolderOffers(status, map[string]bool{folderOfferKey("retro-saves", onboardingPeer): true})
	if !status.FolderOffers[0].Ignored || status.FolderOffers[1].Ignored {
		t.Fatalf("folder offers = %+v", status.FolderOffers)
	}
}
