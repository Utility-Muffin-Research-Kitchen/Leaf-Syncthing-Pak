package controller

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/uicontrol"
)

type uiUpstream interface {
	ReadUIStatus(context.Context, []syncthing.ConfiguredFolder, string) (syncthing.UIStatus, error)
	SetFolderPaused(context.Context, string, bool) error
	RescanFolder(context.Context, string) error
	RenameFolder(context.Context, string, string) error
	AddPeer(context.Context, string, string, []string) error
	RenamePeer(context.Context, string, string) error
}

func applyLiveStatus(status uicontrol.Status, live syncthing.UIStatus) uicontrol.Status {
	status.Issues = withoutIssue(status.Issues, "upstream-status-unavailable")
	status.Issues = withoutIssue(status.Issues, "folder-sync-error")
	for index := range status.Folders {
		status.Folders[index].Issues = withoutIssue(status.Folders[index].Issues, "folder-sync-error")
		upstream, ok := live.Folders[status.Folders[index].ID]
		if !ok {
			continue
		}
		status.Folders[index].LocalBytes = upstream.LocalBytes
		status.Folders[index].GlobalBytes = upstream.GlobalBytes
		status.Folders[index].LocalItems = upstream.LocalItems
		status.Folders[index].GlobalItems = upstream.GlobalItems
		status.Folders[index].LastSync = upstream.LastActivity
		if len(status.Folders[index].Issues) == 0 {
			if status.Folders[index].Paused {
				status.Folders[index].State = "paused"
			} else {
				status.Folders[index].State = upstream.State
			}
		}
		if upstream.ErrorCount > 0 || upstream.PullErrors > 0 {
			issue := uicontrol.Issue{Code: "folder-sync-error", Message: "Syncthing reports errors for this folder", Scope: "folder", SubjectID: status.Folders[index].ID}
			status.Folders[index].Issues = appendIssue(status.Folders[index].Issues, issue)
			status.Issues = appendIssue(status.Issues, issue)
		}
	}
	status.Peers = make([]uicontrol.PeerStatus, 0, len(live.Peers))
	for _, peer := range live.Peers {
		status.Peers = append(status.Peers, uicontrol.PeerStatus{
			ID: peer.ID, IDSuffix: deviceIDSuffix(peer.ID), Name: peer.Name, State: peer.State,
			Connection: peer.Connection, Address: peer.Address, Paused: peer.Paused,
			Introducer: peer.Introducer, IntroducedBy: peer.IntroducedBy, Pending: peer.Pending,
		})
	}
	status.FolderOffers = make([]uicontrol.FolderOfferStatus, 0, len(live.FolderOffers))
	for _, offer := range live.FolderOffers {
		status.FolderOffers = append(status.FolderOffers, uicontrol.FolderOfferStatus{
			FolderID: offer.FolderID, Label: offer.Label, DeviceID: offer.DeviceID,
			DeviceIDSuffix: deviceIDSuffix(offer.DeviceID), DeviceName: offer.DeviceName,
			OfferedAt: offer.OfferedAt, ReceiveEncrypted: offer.ReceiveEncrypted,
			RemoteEncrypted: offer.RemoteEncrypted,
		})
	}
	status.Transfer = &uicontrol.TransferStatus{
		State: live.Transfer.State, LocalBytes: live.Transfer.LocalBytes,
		GlobalBytes: live.Transfer.GlobalBytes, NeedBytes: live.Transfer.NeedBytes,
		InBytes: live.Transfer.InBytes, OutBytes: live.Transfer.OutBytes,
	}
	return status
}

func applyLiveStatusError(status uicontrol.Status) uicontrol.Status {
	issue := uicontrol.Issue{
		Code: "upstream-status-unavailable", Message: "Live Syncthing status is temporarily unavailable",
		Scope: "controller", SubjectID: ServiceID,
	}
	status.Issues = appendIssue(status.Issues, issue)
	return status
}

func findFolder(status uicontrol.Status, folderID string) (*uicontrol.FolderStatus, bool) {
	for index := range status.Folders {
		if status.Folders[index].ID == folderID {
			return &status.Folders[index], true
		}
	}
	return nil, false
}

func findFolderOffer(status uicontrol.Status, folderID, deviceID string) (uicontrol.FolderOfferStatus, bool) {
	for _, offer := range status.FolderOffers {
		if offer.FolderID == folderID && offer.DeviceID == deviceID {
			return offer, true
		}
	}
	return uicontrol.FolderOfferStatus{}, false
}

func folderSafeForAction(folder *uicontrol.FolderStatus) bool {
	if folder == nil || folder.CardID == "" {
		return false
	}
	for _, issue := range folder.Issues {
		switch issue.Code {
		case "folder-sync-error":
		default:
			return false
		}
	}
	return true
}

func folderSafeForInspect(folder *uicontrol.FolderStatus) bool {
	if folder == nil || folder.CardID == "" || !filepath.IsAbs(folder.Path) {
		return false
	}
	for _, issue := range folder.Issues {
		switch issue.Code {
		case "folder-sync-error", "folder-conflicts":
		default:
			return false
		}
	}
	return true
}

func withoutSubjectIssue(issues []uicontrol.Issue, code, subjectID string) []uicontrol.Issue {
	result := make([]uicontrol.Issue, 0, len(issues))
	for _, issue := range issues {
		if issue.Code != code || issue.SubjectID != subjectID {
			result = append(result, issue)
		}
	}
	return result
}

func onlyManualPause(reasons []string) bool {
	for _, reason := range reasons {
		if reason != "manual" {
			return false
		}
	}
	return true
}

func appendIssue(issues []uicontrol.Issue, issue uicontrol.Issue) []uicontrol.Issue {
	for _, existing := range issues {
		if existing.Code == issue.Code && existing.Scope == issue.Scope && existing.SubjectID == issue.SubjectID {
			return issues
		}
	}
	return append(issues, issue)
}

func withoutIssue(issues []uicontrol.Issue, code string) []uicontrol.Issue {
	result := make([]uicontrol.Issue, 0, len(issues))
	for _, issue := range issues {
		if issue.Code != code {
			result = append(result, issue)
		}
	}
	return result
}

func deviceIDSuffix(deviceID string) string {
	deviceID = strings.ReplaceAll(deviceID, "-", "")
	if len(deviceID) <= 7 {
		return deviceID
	}
	return deviceID[len(deviceID)-7:]
}
