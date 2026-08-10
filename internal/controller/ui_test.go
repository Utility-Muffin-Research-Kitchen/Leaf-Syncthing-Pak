package controller

import (
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
