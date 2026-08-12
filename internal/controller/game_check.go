package controller

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/cards"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/life1"
	syncthingconfig "github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/uicontrol"
)

// The initial game.check reply must fit inside LIFE-1's ack_ms. Use the protocol
// ceiling for this controller and reserve a final slice for writing the reply;
// the rest bounds retries while upstream's REST surface is still coming up.
const (
	gameCheckAckMS         = 1000
	firstStatusBudget      = 900 * time.Millisecond
	firstStatusProbeBudget = 700 * time.Millisecond
	firstStatusReplyMargin = 100 * time.Millisecond
	firstStatusRetryDelay  = 100 * time.Millisecond
)

func foldersForGameCheck(event life1.Event, inventory []cards.Card, folders []syncthingconfig.ConfiguredFolder, controls map[string]folderControlRecord) ([]syncthingconfig.ConfiguredFolder, error) {
	var card *cards.Card
	for index := range inventory {
		candidate := &inventory[index]
		if candidate.Source.ID != event.SourceID {
			continue
		}
		if card != nil {
			return nil, errors.New("game source resolves to multiple cards")
		}
		card = candidate
	}
	if card == nil || card.Identity.ID == "" || card.State != cards.StateEnrolled ||
		!card.Present || !card.Writable || card.DuplicateID {
		return nil, errors.New("game source does not resolve to one enrolled present card")
	}
	if filepath.Clean(event.SavesPath) != filepath.Clean(managedContentPath(card.Source, "saves")) ||
		filepath.Clean(event.StatesPath) != filepath.Clean(managedContentPath(card.Source, "states")) {
		return nil, errors.New("game paths do not match the resolved card")
	}

	configured := make(map[string]syncthingconfig.ConfiguredFolder, len(folders))
	for _, folder := range folders {
		configured[folder.ID] = folder
	}
	rows, _ := reconcileManagedFolders(folders, inventory, controls)
	safeRows := make(map[string]uicontrol.FolderStatus, len(rows))
	for _, row := range rows {
		safeRows[row.ID] = row
	}
	selected := make([]syncthingconfig.ConfiguredFolder, 0, 2)
	for folderID, control := range controls {
		if control.CardID != card.Identity.ID || (control.Kind != "saves" && control.Kind != "states") {
			continue
		}
		if !completeFolderBinding(control) || control.FirstSync || control.PendingAdd ||
			control.PendingMembership != "" || control.PendingStop {
			return nil, fmt.Errorf("managed %s folder is not ready", control.Kind)
		}
		folder, found := configured[folderID]
		if !found || folder.Kind != control.Kind ||
			filepath.Clean(folder.Path) != filepath.Clean(managedContentPath(card.Source, control.Kind)) {
			return nil, fmt.Errorf("managed %s folder does not match its card binding", control.Kind)
		}
		row, safe := safeRows[folderID]
		if !safe || row.Paused || !folderSafeForAction(&row) {
			return nil, fmt.Errorf("managed %s folder is not safe to check", control.Kind)
		}
		selected = append(selected, folder)
	}
	return selected, nil
}

func runGameCheck(ctx context.Context, lifecycle Lifecycle, upstream gameCheckUpstream, folders []syncthingconfig.ConfiguredFolder, selfDeviceID string, event life1.Event, ackMS int, logf func(string, ...any)) {
	if upstream == nil {
		_ = lifecycle.SendError(event.LaunchID, "check-unavailable")
		return
	}
	if ackMS <= 0 {
		ackMS = life1.DefaultAckMS
	}
	var last syncthingconfig.GameCheckStatus
	haveLast := false
	syntheticWaiting := false
	// A launch that arrives while upstream's status endpoint is still coming up
	// used to fail closed on the very first read error. Both an error reply and
	// an ack timeout land the launch on Needs attention, so retrying inside a
	// short budget can only improve the outcome — it never converts a real
	// failure into a launch.
	ackBudget := time.Duration(ackMS) * time.Millisecond
	replyMargin := firstStatusReplyMargin
	if ackBudget <= replyMargin {
		replyMargin = ackBudget / 5
	}
	firstReplyBudget := ackBudget - replyMargin
	if firstReplyBudget > firstStatusBudget {
		firstReplyBudget = firstStatusBudget
	}
	firstReadDeadline := time.Now().Add(firstReplyBudget)
	for {
		readTimeout := time.Duration(ackMS) * time.Millisecond
		if haveLast && readTimeout < 2*time.Second {
			readTimeout = 2 * time.Second
		} else if !haveLast {
			remaining := time.Until(firstReadDeadline)
			if remaining <= 0 {
				_ = lifecycle.SendError(event.LaunchID, "sync-status-unavailable")
				return
			}
			if readTimeout > remaining {
				readTimeout = remaining
			}
			if readTimeout > firstStatusProbeBudget {
				readTimeout = firstStatusProbeBudget
			}
		}
		checkContext, cancel := context.WithTimeout(ctx, readTimeout)
		current, err := upstream.ReadGameCheckStatus(checkContext, folders, selfDeviceID)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if logf != nil {
				logf("check-before-stop needs attention: %v", err)
			}
			if !haveLast {
				if errors.Is(err, context.DeadlineExceeded) && time.Until(firstReadDeadline) > 0 {
					last = syncthingconfig.GameCheckStatus{PendingItems: 1}
					if err := lifecycle.SendWaiting(event.LaunchID, last.PendingItems, last.PendingBytes); err != nil {
						return
					}
					haveLast = true
					syntheticWaiting = true
					continue
				}
				if remaining := time.Until(firstReadDeadline); remaining > 0 {
					if remaining > firstStatusRetryDelay {
						remaining = firstStatusRetryDelay
					}
					timer := time.NewTimer(remaining)
					select {
					case <-ctx.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
					continue
				}
				_ = lifecycle.SendError(event.LaunchID, "sync-status-unavailable")
				return
			}
			if syntheticWaiting {
				_ = lifecycle.SendError(event.LaunchID, "sync-status-unavailable")
				return
			}
		} else {
			if ctx.Err() != nil {
				return
			}
			if current.Current {
				_ = lifecycle.SendStop(event.LaunchID)
				return
			}
			if !haveLast || current.PendingItems != last.PendingItems || current.PendingBytes != last.PendingBytes {
				if err := lifecycle.SendWaiting(event.LaunchID, current.PendingItems, current.PendingBytes); err != nil {
					return
				}
				last = current
				haveLast = true
			}
			syntheticWaiting = false
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
