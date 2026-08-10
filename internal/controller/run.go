package controller

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/cards"
	gatewayserver "github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/gateway"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/leaf"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/life1"
	syncthingconfig "github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/syncthing"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/uicontrol"
)

const StopGrace = 10 * time.Second

type UpstreamProcess interface {
	Done() <-chan error
	Shutdown(context.Context) error
}

type gatewayUpstream interface {
	GatewayTransport() http.RoundTripper
}

type StartProcessFunc func(context.Context, syncthingconfig.ProcessOptions) (UpstreamProcess, error)
type LoadCardsFunc func(leaf.SourceList, string) ([]cards.Card, error)
type EnrollCardFunc func(leaf.Source) (cards.Identity, bool, error)

type lifecycleResult struct {
	event life1.Event
	err   error
}

// Run owns the foreground service lifetime. It never restarts upstream inside
// the same supervised generation; any unexpected upstream or LIFE-1 failure is
// returned to Jawaka for reserved-group cleanup and restart policy.
func (runner Runner) Run(ctx context.Context) error {
	var session *Session
	var err error
	for {
		session, err = runner.Bootstrap(ctx)
		if err == nil {
			break
		}
		if errors.Is(err, ErrLifecycleStop) {
			return nil
		}
		if !errors.Is(err, ErrResetPending) {
			return err
		}
		if err := runner.waitForResetRecovery(ctx); err != nil {
			return err
		}
	}
	defer session.Close()
	folderControls := session.FolderControls
	logging, err := newLoggingManager(
		filepath.Join(runner.Config.UserdataPath, leaf.AppStateName, "leaf", loggingStateName), nil,
	)
	if err != nil {
		return fmt.Errorf("load logging state: %w", err)
	}

	startProcess := runner.StartProcess
	if startProcess == nil {
		startProcess = func(ctx context.Context, options syncthingconfig.ProcessOptions) (UpstreamProcess, error) {
			return syncthingconfig.StartProcess(ctx, options)
		}
	}
	upstream, err := startProcess(ctx, syncthingconfig.ProcessOptions{
		Binary: runner.Config.UpstreamBinary, ConfigDir: runner.Config.ConfigDir,
		DataDir: runner.Config.DataDir, GUISocket: runner.Config.GUISocket,
		Stdout: os.Stdout, Stderr: os.Stderr,
	})
	var foreignConflict syncthingconfig.Conflict
	if err != nil && !errors.As(err, &foreignConflict) {
		return fmt.Errorf("start upstream: %w", err)
	}
	if err != nil {
		upstream = nil
	}
	var network *networkManager
	if networkUpstream, ok := upstream.(networkUpstream); ok {
		network, err = newNetworkManager(runner.Config.UserdataPath, session.Identity.DeviceID, networkUpstream)
		if err == nil {
			networkContext, cancel := context.WithTimeout(ctx, 8*time.Second)
			err = network.Initialize(networkContext)
			cancel()
		}
		if err != nil {
			_ = shutdownUpstream(upstream)
			return fmt.Errorf("enforce initial network profile: %w", err)
		}
	}
	deviceUI, _ := upstream.(uiUpstream)
	b3Folders, _ := upstream.(b3FolderUpstream)
	onboarding := newOnboardingManager(runner.OnboardingOptions)
	var browserGateway *gatewayserver.Manager
	if gatewayUpstream, ok := upstream.(gatewayUpstream); ok {
		transport := gatewayUpstream.GatewayTransport()
		if transport != nil {
			browserGateway, err = gatewayserver.New(gatewayserver.Options{
				StateDirectory: filepath.Join(runner.Config.UserdataPath, leaf.AppStateName, "leaf"),
				Upstream:       transport, Port: 8384,
				Addresses: func() ([]net.IP, error) {
					return syncthingconfig.EligibleLANAddresses(syncthingconfig.DefaultRouteFiles())
				},
			})
			if err != nil {
				_ = shutdownUpstream(upstream)
				return fmt.Errorf("initialize browser gateway: %w", err)
			}
			defer browserGateway.Close()
		}
	}
	loadCards := runner.LoadCards
	if loadCards == nil {
		loadCards = func(sources leaf.SourceList, registryDirectory string) ([]cards.Card, error) {
			live, err := cards.InspectSources(sources, cards.Options{})
			if err != nil {
				return nil, err
			}
			return cards.ReconcileRegistry(registryDirectory, sources, live, nil)
		}
	}
	registryDirectory := filepath.Join(runner.Config.UserdataPath, leaf.AppStateName, "leaf")
	cardInventory, err := loadCards(runner.Config.Sources, registryDirectory)
	if err != nil {
		_ = shutdownUpstream(upstream)
		return fmt.Errorf("verify cards: %w", err)
	}
	if hasPendingMembership(folderControls.Snapshot()) {
		if b3Folders == nil {
			_ = shutdownUpstream(upstream)
			return errors.New("recover folder membership: upstream folder control is unavailable")
		}
		recoveryContext, recoveryCancel := context.WithTimeout(ctx, 15*time.Second)
		session.Folders, err = recoverFolderMemberships(
			recoveryContext, session.Folders, session.Identity.DeviceID, cardInventory, folderControls, b3Folders,
		)
		recoveryCancel()
		if err != nil {
			_ = shutdownUpstream(upstream)
			return fmt.Errorf("recover folder membership: %w", err)
		}
	}
	gameState := session.State
	var status atomic.Value
	initialStatus := controlStatus(session, gameState, cardInventory, folderControls.Snapshot())
	initialStatus = applyFirstSyncStatus(initialStatus, session.FirstSync, folderControls)
	loggingStatus := logging.Status()
	initialStatus.Logging = &loggingStatus
	initialStatus.Diagnostics = &uicontrol.DiagnosticsStatus{}
	if storage, storageErr := storageInventory(cardInventory); storageErr == nil {
		initialStatus.Storage = &storage
	} else {
		initialStatus.Issues = appendIssue(initialStatus.Issues, uicontrol.Issue{
			Code: "storage-inventory-unavailable", Message: "Snapshot and version inventory is unavailable because retained state is unsafe",
			Scope: "controller", SubjectID: ServiceID,
		})
	}
	initialStatus.Capabilities = append(initialStatus.Capabilities,
		uicontrol.OperationResetPrepare, uicontrol.OperationLogLevelSet, uicontrol.OperationDiagnosticsExport)
	if network != nil {
		networkStatus := network.Status()
		initialStatus.Network = &networkStatus
		initialStatus.Capabilities = append(initialStatus.Capabilities, uicontrol.OperationNetworkSet)
	}
	if browserGateway != nil {
		gatewayStatus := controlGatewayStatus(browserGateway.Status())
		initialStatus.Gateway = &gatewayStatus
		initialStatus.Capabilities = append(initialStatus.Capabilities,
			uicontrol.OperationGatewayOpen, uicontrol.OperationGatewayKeepAlive,
			uicontrol.OperationGatewayClose, uicontrol.OperationGatewayExtend,
			uicontrol.OperationGatewayRevoke)
	}
	if deviceUI != nil {
		initialStatus.Capabilities = append(initialStatus.Capabilities,
			uicontrol.OperationFolderPause, uicontrol.OperationFolderResume,
			uicontrol.OperationFolderRescan, uicontrol.OperationFolderRename, uicontrol.OperationFolderInspect,
			uicontrol.OperationDeviceAdd, uicontrol.OperationDeviceRename)
	}
	if b3Folders != nil {
		initialStatus.Capabilities = append(initialStatus.Capabilities,
			uicontrol.OperationFolderOnboardPlan, uicontrol.OperationFolderOfferPlan,
			uicontrol.OperationFolderOfferIgnore, uicontrol.OperationFolderOfferRestore,
			uicontrol.OperationFolderOnboardCreate,
			uicontrol.OperationFolderFirstSyncPrepare, uicontrol.OperationFolderFirstSyncStart,
			uicontrol.OperationFolderTypeSet, uicontrol.OperationFolderShare, uicontrol.OperationFolderUnshare)
	}
	if !foreignConflict.Empty() {
		initialStatus.Upstream.State = "conflict"
		initialStatus.Issues = append(initialStatus.Issues, uicontrol.Issue{
			Code: "foreign-syncthing", Message: foreignConflict.Error(), Scope: "controller", SubjectID: ServiceID,
		})
	}
	status.Store(initialStatus)
	refreshUIStatus := func() uicontrol.Status {
		current := status.Load().(uicontrol.Status)
		loggingStatus := logging.Status()
		current.Logging = &loggingStatus
		if deviceUI != nil && current.Upstream.State == "running" {
			refreshContext, refreshCancel := context.WithTimeout(ctx, 1500*time.Millisecond)
			live, refreshErr := deviceUI.ReadUIStatus(refreshContext, session.Folders, session.Identity.DeviceID)
			refreshCancel()
			if refreshErr != nil {
				current = applyLiveStatusError(current)
			} else {
				current = applyLiveStatus(current, live)
				current = applyIgnoredFolderOffers(current, folderControls.IgnoredOffers())
			}
		}
		current = applyFirstSyncStatus(current, session.FirstSync, folderControls)
		status.Store(current)
		return current
	}
	enrollCard := runner.EnrollCard
	if enrollCard == nil {
		enrollCard = func(source leaf.Source) (cards.Identity, bool, error) {
			return cards.Enroll(source, cards.Options{})
		}
	}
	control, err := uicontrol.Listen(runner.Config.ControlSocket, uicontrol.Operations{
		Status: refreshUIStatus,
		EnrollCard: func(sourceID string) (uicontrol.Status, *uicontrol.ProtocolError) {
			source, found := runner.Config.Sources.ByID(sourceID)
			if !found {
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "not-found", Message: "The requested card slot is not configured"}
			}
			if _, _, err := enrollCard(source); err != nil {
				return uicontrol.Status{}, cardOperationError(err)
			}
			inventory, err := loadCards(runner.Config.Sources, registryDirectory)
			if err != nil {
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "operation-failed", Message: "The card was enrolled but inventory refresh failed"}
			}
			updated := applyInventory(status.Load().(uicontrol.Status), inventory, session.Folders, folderControls.Snapshot())
			updated = applyFirstSyncStatus(updated, session.FirstSync, folderControls)
			if storage, storageErr := storageInventory(inventory); storageErr == nil {
				updated.Storage = &storage
			}
			status.Store(updated)
			return updated, nil
		},
		PlanFolder: func(sourceID, kind, folderType string, deviceIDs []string) (uicontrol.Status, *uicontrol.ProtocolError) {
			if b3Folders == nil {
				return uicontrol.Status{}, b3OperationError(errors.New("folder setup is unavailable"))
			}
			inventory, inventoryErr := loadCards(runner.Config.Sources, registryDirectory)
			if inventoryErr != nil {
				return uicontrol.Status{}, b3OperationError(inventoryErr)
			}
			planContext, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			plan, planErr := onboarding.Plan(planContext, sourceID, kind, folderType, session.Identity.DeviceID, deviceIDs, inventory, session.Folders, b3Folders)
			if planErr != nil {
				return uicontrol.Status{}, b3OperationError(planErr)
			}
			updated := applyInventory(status.Load().(uicontrol.Status), inventory, session.Folders, folderControls.Snapshot())
			updated = applyFirstSyncStatus(updated, session.FirstSync, folderControls)
			updated.Onboarding = controlOnboardingStatus(plan)
			status.Store(updated)
			return updated, nil
		},
		PlanFolderOffer: func(folderID, deviceID, sourceID, kind, folderType string) (uicontrol.Status, *uicontrol.ProtocolError) {
			if b3Folders == nil {
				return uicontrol.Status{}, b3OperationError(errors.New("folder setup is unavailable"))
			}
			current := refreshUIStatus()
			offer, found := findFolderOffer(current, folderID, deviceID)
			if !found {
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "not-found", Message: "The folder offer is no longer available"}
			}
			if offer.Ignored {
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "operation-failed", Message: "Restore this ignored folder offer before reviewing it"}
			}
			if offer.ReceiveEncrypted || offer.RemoteEncrypted {
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "operation-failed", Message: "Encrypted folder offers are not supported by Leaf"}
			}
			inventory, inventoryErr := loadCards(runner.Config.Sources, registryDirectory)
			if inventoryErr != nil {
				return uicontrol.Status{}, b3OperationError(inventoryErr)
			}
			planContext, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			plan, planErr := onboarding.PlanOffer(
				planContext, sourceID, kind, folderType, offer.FolderID, offer.Label, offer.DeviceID,
				session.Identity.DeviceID, inventory, session.Folders, b3Folders,
			)
			if planErr != nil {
				return uicontrol.Status{}, b3OperationError(planErr)
			}
			updated := applyInventory(current, inventory, session.Folders, folderControls.Snapshot())
			updated = applyFirstSyncStatus(updated, session.FirstSync, folderControls)
			updated.Onboarding = controlOnboardingStatus(plan)
			status.Store(updated)
			return updated, nil
		},
		FolderOfferAction: func(operation, folderID, deviceID string) (uicontrol.Status, *uicontrol.ProtocolError) {
			current := refreshUIStatus()
			if _, found := findFolderOffer(current, folderID, deviceID); !found {
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "not-found", Message: "The folder offer is no longer available"}
			}
			ignored := operation == uicontrol.OperationFolderOfferIgnore
			if operation != uicontrol.OperationFolderOfferIgnore && operation != uicontrol.OperationFolderOfferRestore {
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "unsupported-op", Message: "Unsupported folder offer operation"}
			}
			if err := folderControls.SetOfferIgnored(folderID, deviceID, ignored); err != nil {
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "operation-failed", Message: "The folder offer preference could not be stored safely"}
			}
			return refreshUIStatus(), nil
		},
		CreateFolder: func(planID string, statesAcknowledged, manualAcknowledged bool) (uicontrol.Status, *uicontrol.ProtocolError) {
			if b3Folders == nil {
				return uicontrol.Status{}, b3OperationError(errors.New("folder setup is unavailable"))
			}
			inventory, inventoryErr := loadCards(runner.Config.Sources, registryDirectory)
			if inventoryErr != nil {
				return uicontrol.Status{}, b3OperationError(inventoryErr)
			}
			createContext, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			folder, createErr := onboarding.Create(createContext, planID, session.Identity.DeviceID,
				statesAcknowledged, manualAcknowledged, inventory, session.Folders, folderControls, b3Folders)
			if createErr != nil {
				return uicontrol.Status{}, b3OperationError(createErr)
			}
			session.Folders = append(session.Folders, folder)
			session.FirstSync.Register(folder.ID)
			updated := applyInventory(status.Load().(uicontrol.Status), inventory, session.Folders, folderControls.Snapshot())
			updated = applyFirstSyncStatus(updated, session.FirstSync, folderControls)
			updated.Onboarding = nil
			if storage, storageErr := storageInventory(inventory); storageErr == nil {
				updated.Storage = &storage
			}
			status.Store(updated)
			return refreshUIStatus(), nil
		},
		PrepareFirstSync: func(folderID string) (uicontrol.Status, *uicontrol.ProtocolError) {
			if b3Folders == nil {
				return uicontrol.Status{}, b3OperationError(errors.New("first sync is unavailable"))
			}
			current := refreshUIStatus()
			row, rowFound := findFolder(current, folderID)
			folder, _, configured := findConfiguredFolder(session.Folders, folderID)
			controlState := folderControls.Snapshot()[folderID]
			if !rowFound || !configured || !folderSafeForAction(row) || !controlState.FirstSync || folder.Type == "sendonly" {
				return uicontrol.Status{}, b3OperationError(errors.New("this folder is not ready for a receive-capable first-sync snapshot"))
			}
			inventory, inventoryErr := loadCards(runner.Config.Sources, registryDirectory)
			if inventoryErr != nil {
				return uicontrol.Status{}, b3OperationError(inventoryErr)
			}
			card, cardOK := cardForConfiguredFolder(folder, inventory, folderControls.Snapshot())
			if !cardOK || !usableEnrolledCard(card) {
				return uicontrol.Status{}, b3OperationError(errors.New("the enrolled physical card is unavailable"))
			}
			prepareContext, cancel := context.WithTimeout(ctx, 30*time.Minute)
			defer cancel()
			if _, prepareErr := session.FirstSync.Prepare(prepareContext, folder, card, folderControls); prepareErr != nil {
				return uicontrol.Status{}, b3OperationError(prepareErr)
			}
			updated := applyInventory(status.Load().(uicontrol.Status), inventory, session.Folders, folderControls.Snapshot())
			updated = applyFirstSyncStatus(updated, session.FirstSync, folderControls)
			if storage, storageErr := storageInventory(inventory); storageErr == nil {
				updated.Storage = &storage
			}
			status.Store(updated)
			return refreshUIStatus(), nil
		},
		StartFirstSync: func(folderID string, hubAcknowledged bool) (uicontrol.Status, *uicontrol.ProtocolError) {
			if b3Folders == nil {
				return uicontrol.Status{}, b3OperationError(errors.New("first sync is unavailable"))
			}
			folder, folderIndex, configured := findConfiguredFolder(session.Folders, folderID)
			if !configured || !folderControls.Snapshot()[folderID].FirstSync {
				return uicontrol.Status{}, b3OperationError(errors.New("this folder does not have a pending first sync"))
			}
			inventory, inventoryErr := loadCards(runner.Config.Sources, registryDirectory)
			if inventoryErr != nil {
				return uicontrol.Status{}, b3OperationError(inventoryErr)
			}
			card, cardOK := cardForConfiguredFolder(folder, inventory, folderControls.Snapshot())
			if !cardOK || !usableEnrolledCard(card) {
				return uicontrol.Status{}, b3OperationError(errors.New("the enrolled physical card is unavailable"))
			}
			if completeErr := session.FirstSync.Complete(folder, card, folderControls, hubAcknowledged); completeErr != nil {
				return uicontrol.Status{}, b3OperationError(completeErr)
			}
			pauseSet := requiredOfflinePauseSet(session.Folders, inventory, folderControls.Snapshot())
			if !pauseSet[folderID] {
				actionContext, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				if unpauseErr := b3Folders.SetFolderPaused(actionContext, folderID, false); unpauseErr != nil {
					return uicontrol.Status{}, b3OperationError(unpauseErr)
				}
				session.Folders[folderIndex].Paused = false
				if folderControls.Snapshot()[folderID].PendingRescan {
					if rescanErr := b3Folders.RescanFolder(actionContext, folderID); rescanErr != nil {
						return uicontrol.Status{}, b3OperationError(rescanErr)
					}
					if stateErr := folderControls.SetPendingRescan(folderID, false); stateErr != nil {
						return uicontrol.Status{}, b3OperationError(stateErr)
					}
				}
			}
			updated := applyInventory(status.Load().(uicontrol.Status), inventory, session.Folders, folderControls.Snapshot())
			updated = applyFirstSyncStatus(updated, session.FirstSync, folderControls)
			if storage, storageErr := storageInventory(inventory); storageErr == nil {
				updated.Storage = &storage
			}
			status.Store(updated)
			return refreshUIStatus(), nil
		},
		SetFolderType: func(folderID, folderType string) (uicontrol.Status, *uicontrol.ProtocolError) {
			if b3Folders == nil {
				return uicontrol.Status{}, b3OperationError(errors.New("folder type control is unavailable"))
			}
			current := refreshUIStatus()
			row, rowFound := findFolder(current, folderID)
			folder, folderIndex, configured := findConfiguredFolder(session.Folders, folderID)
			if !rowFound || !configured || !folderSafeForAction(row) {
				return uicontrol.Status{}, b3OperationError(errors.New("the folder safety checks must pass before changing its type"))
			}
			if folder.Type == folderType {
				return current, nil
			}
			inventory, inventoryErr := loadCards(runner.Config.Sources, registryDirectory)
			if inventoryErr != nil {
				return uicontrol.Status{}, b3OperationError(inventoryErr)
			}
			card, cardOK := cardForConfiguredFolder(folder, inventory, folderControls.Snapshot())
			if !cardOK || !usableEnrolledCard(card) {
				return uicontrol.Status{}, b3OperationError(errors.New("the enrolled physical card is unavailable"))
			}
			actionContext, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if pauseErr := b3Folders.SetFolderPaused(actionContext, folderID, true); pauseErr != nil {
				return uicontrol.Status{}, b3OperationError(pauseErr)
			}
			crossesSendOnly := (folder.Type == "sendonly") != (folderType == "sendonly")
			if crossesSendOnly {
				if invalidateErr := session.FirstSync.Invalidate(folder, card, folderControls); invalidateErr != nil {
					return uicontrol.Status{}, b3OperationError(invalidateErr)
				}
			}
			target := folder
			target.Type = folderType
			target.Paused = true
			if folderType != "sendonly" {
				target.VersioningType = "simple"
				target.VersioningFSPath = filepath.Join(card.Source.UserdataPath, leaf.AppStateName, "versions", folder.Kind)
				target.VersioningFSType = "basic"
				if err := ensureSafeDirectoryChain(card.Source.Root, target.VersioningFSPath); err != nil {
					return uicontrol.Status{}, b3OperationError(err)
				}
			}
			if typeErr := b3Folders.SetManagedFolderType(actionContext, target); typeErr != nil {
				return uicontrol.Status{}, b3OperationError(typeErr)
			}
			session.Folders[folderIndex] = target
			pauseSet := requiredOfflinePauseSet(session.Folders, inventory, folderControls.Snapshot())
			if !pauseSet[folderID] {
				if unpauseErr := b3Folders.SetFolderPaused(actionContext, folderID, false); unpauseErr != nil {
					return uicontrol.Status{}, b3OperationError(unpauseErr)
				}
				session.Folders[folderIndex].Paused = false
			}
			updated := applyInventory(status.Load().(uicontrol.Status), inventory, session.Folders, folderControls.Snapshot())
			updated = applyFirstSyncStatus(updated, session.FirstSync, folderControls)
			status.Store(updated)
			return refreshUIStatus(), nil
		},
		FolderMembership: func(operation, folderID, deviceID string) (uicontrol.Status, *uicontrol.ProtocolError) {
			if b3Folders == nil {
				return uicontrol.Status{}, b3OperationError(errors.New("folder sharing is unavailable"))
			}
			deviceID, normalizeErr := syncthingconfig.NormalizeDeviceID(deviceID)
			if normalizeErr != nil {
				return uicontrol.Status{}, b3OperationError(errors.New("the selected peer identity is invalid"))
			}
			current := refreshUIStatus()
			row, rowFound := findFolder(current, folderID)
			peer, peerFound := findPeer(current, deviceID)
			folder, folderIndex, configured := findConfiguredFolder(session.Folders, folderID)
			if !rowFound || !configured || !folderSafeForAction(row) {
				return uicontrol.Status{}, b3OperationError(errors.New("the folder safety checks must pass before changing sharing"))
			}
			if !peerFound || peer.Pending {
				return uicontrol.Status{}, b3OperationError(errors.New("sharing requires an already configured peer"))
			}
			present := operation == uicontrol.OperationFolderShare
			intent := "unshare"
			if present {
				intent = "share"
			}
			target, changed, membershipErr := folderWithMembership(folder, session.Identity.DeviceID, deviceID, present)
			if membershipErr != nil {
				return uicontrol.Status{}, b3OperationError(membershipErr)
			}
			record := folderControls.Snapshot()[folderID]
			if !changed {
				if record.PendingMembership == "" {
					return current, nil
				}
				if record.PendingMembership != intent || record.PendingDeviceID != deviceID {
					return uicontrol.Status{}, b3OperationError(errors.New("another folder membership change is pending"))
				}
			}
			inventory, inventoryErr := loadCards(runner.Config.Sources, registryDirectory)
			if inventoryErr != nil {
				return uicontrol.Status{}, b3OperationError(inventoryErr)
			}
			if intentErr := folderControls.BeginMembership(folderID, deviceID, intent); intentErr != nil {
				return uicontrol.Status{}, b3OperationError(intentErr)
			}
			pending := applyInventory(current, inventory, session.Folders, folderControls.Snapshot())
			pending = applyFirstSyncStatus(pending, session.FirstSync, folderControls)
			status.Store(pending)
			actionContext, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			if membershipErr := b3Folders.SetManagedFolderDevices(actionContext, target); membershipErr != nil {
				return uicontrol.Status{}, b3OperationError(membershipErr)
			}
			session.Folders[folderIndex] = target
			if intentErr := folderControls.CompleteMembership(folderID); intentErr != nil {
				return uicontrol.Status{}, b3OperationError(intentErr)
			}
			pauseSet := requiredOfflinePauseSet(session.Folders, inventory, folderControls.Snapshot())
			if !pauseSet[folderID] {
				if unpauseErr := b3Folders.SetFolderPaused(actionContext, folderID, false); unpauseErr != nil {
					return uicontrol.Status{}, b3OperationError(unpauseErr)
				}
				session.Folders[folderIndex].Paused = false
			}
			updated := applyInventory(status.Load().(uicontrol.Status), inventory, session.Folders, folderControls.Snapshot())
			updated = applyFirstSyncStatus(updated, session.FirstSync, folderControls)
			status.Store(updated)
			return refreshUIStatus(), nil
		},
		SetNetworkProfile: func(profile string) (uicontrol.Status, *uicontrol.ProtocolError) {
			if network == nil {
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "operation-failed", Message: "Network control is unavailable"}
			}
			networkContext, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			if browserGateway != nil {
				browserGateway.Close()
			}
			if err := network.Set(networkContext, profile); err != nil {
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "operation-failed", Message: "The network profile could not be applied safely"}
			}
			updated := status.Load().(uicontrol.Status)
			networkStatus := network.Status()
			updated.Network = &networkStatus
			status.Store(updated)
			return updated, nil
		},
		GatewayAction: func(operation string) (uicontrol.Status, *uicontrol.ProtocolError) {
			if browserGateway == nil {
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "operation-failed", Message: "Web access is unavailable"}
			}
			var gatewayStatus gatewayserver.Status
			var gatewayErr error
			switch operation {
			case uicontrol.OperationGatewayOpen:
				gatewayStatus, gatewayErr = browserGateway.Open()
			case uicontrol.OperationGatewayKeepAlive:
				gatewayStatus, gatewayErr = browserGateway.KeepAlive()
			case uicontrol.OperationGatewayClose:
				gatewayErr = browserGateway.CloseForeground()
				gatewayStatus = browserGateway.Status()
			case uicontrol.OperationGatewayExtend:
				gatewayStatus, gatewayErr = browserGateway.Extend()
			case uicontrol.OperationGatewayRevoke:
				gatewayErr = browserGateway.RevokeAll()
				gatewayStatus = browserGateway.Status()
			default:
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "unsupported-op", Message: "Unsupported web interface operation"}
			}
			if gatewayErr != nil {
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "operation-failed", Message: "The web interface request could not be completed"}
			}
			updated := status.Load().(uicontrol.Status)
			converted := controlGatewayStatus(gatewayStatus)
			updated.Gateway = &converted
			status.Store(updated)
			return updated, nil
		},
		FolderAction: func(operation, folderID, label string) (uicontrol.Status, *uicontrol.ProtocolError) {
			if deviceUI == nil {
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "operation-failed", Message: "Folder control is unavailable"}
			}
			current := refreshUIStatus()
			folder, found := findFolder(current, folderID)
			if !found {
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "not-found", Message: "The managed folder was not found"}
			}
			if !folderSafeForAction(folder) {
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "operation-failed", Message: "The folder safety checks must pass before this action"}
			}
			actionContext, actionCancel := context.WithTimeout(ctx, 8*time.Second)
			defer actionCancel()
			switch operation {
			case uicontrol.OperationFolderPause:
				if err := folderControls.SetManual(folderID, true); err != nil {
					return uicontrol.Status{}, folderOperationFailure()
				}
				if err := deviceUI.SetFolderPaused(actionContext, folderID, true); err != nil {
					return uicontrol.Status{}, folderOperationFailure()
				}
				folder.Paused = true
				folder.State = "paused"
				folder.PauseReasons = appendUnique(folder.PauseReasons, "manual")
			case uicontrol.OperationFolderResume:
				if !onlyManualPause(folder.PauseReasons) {
					return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "operation-failed", Message: "A safety pause still protects this folder"}
				}
				if err := folderControls.SetManual(folderID, false); err != nil {
					return uicontrol.Status{}, folderOperationFailure()
				}
				if err := deviceUI.SetFolderPaused(actionContext, folderID, false); err != nil {
					return uicontrol.Status{}, folderOperationFailure()
				}
				folder.Paused = false
				folder.PauseReasons = nil
				if folder.PendingRescan {
					if err := deviceUI.RescanFolder(actionContext, folderID); err != nil {
						return uicontrol.Status{}, folderOperationFailure()
					}
					if err := folderControls.SetPendingRescan(folderID, false); err != nil {
						return uicontrol.Status{}, folderOperationFailure()
					}
					folder.PendingRescan = false
				}
			case uicontrol.OperationFolderRescan:
				if folder.Paused {
					if err := folderControls.SetPendingRescan(folderID, true); err != nil {
						return uicontrol.Status{}, folderOperationFailure()
					}
					folder.PendingRescan = true
					status.Store(current)
					return current, nil
				}
				if err := deviceUI.RescanFolder(actionContext, folderID); err != nil {
					return uicontrol.Status{}, folderOperationFailure()
				}
			case uicontrol.OperationFolderRename:
				if err := deviceUI.RenameFolder(actionContext, folderID, label); err != nil {
					return uicontrol.Status{}, folderOperationFailure()
				}
				folder.Label = label
				for index := range session.Folders {
					if session.Folders[index].ID == folderID {
						session.Folders[index].Label = label
					}
				}
			default:
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "unsupported-op", Message: "Unsupported folder operation"}
			}
			status.Store(current)
			return refreshUIStatus(), nil
		},
		FolderInspect: func(folderID string) (uicontrol.Status, *uicontrol.ProtocolError) {
			current := refreshUIStatus()
			folder, found := findFolder(current, folderID)
			if !found {
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "not-found", Message: "The managed folder was not found"}
			}
			if !folderSafeForInspect(folder) {
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "operation-failed", Message: "Conflict inspection requires a present, safe managed folder"}
			}
			conflicts, count, inspectErr := scanFolderConflicts(folder.Path)
			if inspectErr != nil {
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "operation-failed", Message: "The bounded conflict scan could not be completed safely"}
			}
			current.Issues = withoutSubjectIssue(current.Issues, "folder-conflicts", folderID)
			folder.Issues = withoutSubjectIssue(folder.Issues, "folder-conflicts", folderID)
			folder.ConflictCount = count
			folder.Conflicts = conflicts
			if count > 0 {
				issue := uicontrol.Issue{Code: "folder-conflicts", Message: fmt.Sprintf("This folder contains %d Syncthing conflict files", count), Scope: "folder", SubjectID: folderID}
				folder.Issues = appendIssue(folder.Issues, issue)
				current.Issues = appendIssue(current.Issues, issue)
			}
			status.Store(current)
			return current, nil
		},
		DeviceAction: func(operation, deviceID, name string) (uicontrol.Status, *uicontrol.ProtocolError) {
			if deviceUI == nil {
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "operation-failed", Message: "Device control is unavailable"}
			}
			actionContext, actionCancel := context.WithTimeout(ctx, 8*time.Second)
			defer actionCancel()
			var actionErr error
			switch operation {
			case uicontrol.OperationDeviceAdd:
				allowed := []string{}
				if network != nil && network.Status().Profile == string(syncthingconfig.NetworkLANOnly) {
					allowed = network.Status().AllowedNetworks
				}
				actionErr = deviceUI.AddPeer(actionContext, deviceID, name, allowed)
			case uicontrol.OperationDeviceRename:
				actionErr = deviceUI.RenamePeer(actionContext, deviceID, name)
			default:
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "unsupported-op", Message: "Unsupported device operation"}
			}
			if actionErr != nil {
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "operation-failed", Message: "The peer could not be updated safely"}
			}
			return refreshUIStatus(), nil
		},
		PrepareReset: func(action string) (uicontrol.Status, *uicontrol.ProtocolError) {
			inventory, inventoryErr := loadCards(runner.Config.Sources, registryDirectory)
			if inventoryErr != nil {
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "operation-failed", Message: "The reset inventory could not be verified"}
			}
			plan, planErr := PrepareResetPlan(runner.Config, inventory, action)
			if planErr != nil {
				if errors.Is(planErr, ErrResetCardAbsent) {
					return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "card-absent", Message: "Full reset requires every enrolled card; choose available state only to retain absent-card data"}
				}
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "operation-failed", Message: "The exact reset plan could not be sealed"}
			}
			if browserGateway != nil {
				browserGateway.Close()
			}
			updated := status.Load().(uicontrol.Status)
			updated.Recovery.PlanID = plan.ActionID
			updated.Recovery.PlanAction = plan.Action
			updated.Recovery.RemovePaths = plan.Remove
			updated.Recovery.RetainedPaths = plan.Retained
			if browserGateway != nil {
				gatewayStatus := controlGatewayStatus(browserGateway.Status())
				updated.Gateway = &gatewayStatus
			}
			status.Store(updated)
			return updated, nil
		},
		SetLogLevel: func(level string) (uicontrol.Status, *uicontrol.ProtocolError) {
			if err := logging.Set(level); err != nil {
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "operation-failed", Message: "The log level could not be stored"}
			}
			updated := status.Load().(uicontrol.Status)
			loggingStatus := logging.Status()
			updated.Logging = &loggingStatus
			status.Store(updated)
			return updated, nil
		},
		ExportDiagnostics: func() (uicontrol.Status, *uicontrol.ProtocolError) {
			updated := refreshUIStatus()
			diagnostics, diagnosticsErr := exportDiagnostics(runner.Config, updated, time.Now())
			if diagnosticsErr != nil {
				return uicontrol.Status{}, &uicontrol.ProtocolError{Code: "operation-failed", Message: "Redacted diagnostics could not be exported"}
			}
			updated.Diagnostics = &diagnostics
			status.Store(updated)
			return updated, nil
		},
	})
	if err != nil {
		_ = shutdownUpstream(upstream)
		return fmt.Errorf("start UI control socket: %w", err)
	}
	defer control.Close()

	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	lifecycleEvents := make(chan lifecycleResult, 1)
	startLifecycleReader := func(lifecycle Lifecycle) {
		go func() {
			for {
				event, err := lifecycle.Next(runContext)
				select {
				case lifecycleEvents <- lifecycleResult{event: event, err: err}:
				case <-runContext.Done():
					return
				}
				if err != nil {
					return
				}
			}
		}()
	}
	startLifecycleReader(session.Lifecycle)
	var upstreamDone <-chan error
	if upstream != nil {
		upstreamDone = upstream.Done()
	}
	var networkChanges <-chan time.Time
	var networkTicker *time.Ticker
	if network != nil || browserGateway != nil {
		networkTicker = time.NewTicker(500 * time.Millisecond)
		defer networkTicker.Stop()
		networkChanges = networkTicker.C
	}
	nextDebugLog := time.Now().Add(30 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return shutdownUpstream(upstream)
		case err := <-upstreamDone:
			if ctx.Err() != nil {
				return shutdownUpstream(upstream)
			}
			return fmt.Errorf("upstream exited unexpectedly: %v", err)
		case err := <-control.Done():
			if err == nil {
				err = errors.New("control socket stopped")
			}
			if shutdownErr := shutdownUpstream(upstream); shutdownErr != nil {
				return fmt.Errorf("UI control socket failed (%v); %w", err, shutdownErr)
			}
			return fmt.Errorf("UI control socket failed: %w", err)
		case <-networkChanges:
			if logging.Debug() && runner.Logf != nil && !time.Now().Before(nextDebugLog) {
				current := status.Load().(uicontrol.Status)
				runner.Logf("debug status: folders=%d peers=%d issues=%d game_active=%t", len(current.Folders), len(current.Peers), len(current.Issues), current.Game.Active)
				nextDebugLog = time.Now().Add(30 * time.Second)
			}
			if network != nil {
				networkContext, networkCancel := context.WithTimeout(ctx, 8*time.Second)
				changed, refreshErr := network.RefreshIfChanged(networkContext)
				networkCancel()
				if refreshErr != nil {
					if shutdownErr := shutdownUpstream(upstream); shutdownErr != nil {
						return fmt.Errorf("refresh LAN boundary (%v); %w", refreshErr, shutdownErr)
					}
					return fmt.Errorf("refresh LAN boundary: %w", refreshErr)
				}
				if changed {
					if browserGateway != nil {
						browserGateway.Close()
					}
					updated := status.Load().(uicontrol.Status)
					networkStatus := network.Status()
					updated.Network = &networkStatus
					if browserGateway != nil {
						gatewayStatus := controlGatewayStatus(browserGateway.Status())
						updated.Gateway = &gatewayStatus
					}
					status.Store(updated)
				}
			}
			if browserGateway != nil {
				closed, _ := browserGateway.Tick()
				if closed {
					updated := status.Load().(uicontrol.Status)
					gatewayStatus := controlGatewayStatus(browserGateway.Status())
					updated.Gateway = &gatewayStatus
					status.Store(updated)
				}
			}
		case result := <-lifecycleEvents:
			if result.err != nil {
				if ctx.Err() != nil {
					return shutdownUpstream(upstream)
				}
				_ = session.Lifecycle.Close()
				replacement, state, err := runner.establishLifecycle(ctx)
				if err != nil {
					if ctx.Err() != nil {
						return shutdownUpstream(upstream)
					}
					if shutdownErr := shutdownUpstream(upstream); shutdownErr != nil {
						return fmt.Errorf("reconnect LIFE-1 (%v); stop upstream: %w", err, shutdownErr)
					}
					return err
				}
				session.Lifecycle = replacement
				if runner.Config.Mode == life1.ModeStop && state.Active {
					return shutdownUpstream(upstream)
				}
				gameState = state
				updated := status.Load().(uicontrol.Status)
				updated.Game = uicontrol.GameStatus{Active: state.Active, LaunchID: state.LaunchID, SourceID: state.SourceID}
				status.Store(updated)
				startLifecycleReader(replacement)
				continue
			}
			if err := handleLifecycleEvent(session.Lifecycle, result.event); err != nil {
				return err
			}
			gameState = reconcileGameState(gameState, result.event)
			updated := status.Load().(uicontrol.Status)
			updated.Game = uicontrol.GameStatus{Active: gameState.Active, LaunchID: gameState.LaunchID, SourceID: gameState.SourceID}
			status.Store(updated)
		}
	}
}

func (runner Runner) waitForResetRecovery(ctx context.Context) error {
	status := uicontrol.Status{
		Controller: "recovery-pending",
		Upstream:   uicontrol.UpstreamStatus{State: "stopped", Version: runner.Config.UpstreamVersion},
		Game:       uicontrol.GameStatus{}, Recovery: uicontrol.RecoveryStatus{State: "pending"},
		Cards: []uicontrol.CardStatus{}, Folders: []uicontrol.FolderStatus{},
		Issues: []uicontrol.Issue{{
			Code: "reset-recovery-pending", Message: "Reset recovery is waiting for an enrolled card named in the durable reset intent",
			Scope: "controller", SubjectID: ServiceID,
		}},
		Capabilities: []string{uicontrol.OperationGet},
	}
	control, err := uicontrol.Listen(runner.Config.ControlSocket, uicontrol.Operations{
		Status: func() uicontrol.Status { return status },
	})
	if err != nil {
		return fmt.Errorf("start reset-recovery UI control socket: %w", err)
	}
	defer control.Close()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-control.Done():
			if err == nil {
				err = errors.New("control socket stopped")
			}
			return fmt.Errorf("reset-recovery UI control socket failed: %w", err)
		case <-ticker.C:
			_, recoveryErr := RecoverReset(runner.Config, ResetOptions{})
			if recoveryErr == nil {
				return nil
			}
			if !errors.Is(recoveryErr, ErrResetPending) {
				return fmt.Errorf("recover destructive reset: %w", recoveryErr)
			}
		}
	}
}

func folderOperationFailure() *uicontrol.ProtocolError {
	return &uicontrol.ProtocolError{Code: "operation-failed", Message: "The folder request could not be completed safely"}
}

func b3OperationError(err error) *uicontrol.ProtocolError {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, cards.ErrForeignMarker):
		return &uicontrol.ProtocolError{Code: "foreign-folder-manager", Message: "A default .stfolder shows that another Syncthing manages this directory"}
	case strings.Contains(message, "insufficient free space") || strings.Contains(message, "enospc"):
		return &uicontrol.ProtocolError{Code: "insufficient-space", Message: "The card does not have enough free space for a safety snapshot; choose send-only or cancel"}
	case strings.Contains(message, "at least one syncthing peer"):
		return &uicontrol.ProtocolError{Code: "no-peers", Message: "Add at least one Syncthing peer before creating a folder"}
	case strings.Contains(message, "absent or expired"):
		return &uicontrol.ProtocolError{Code: "plan-expired", Message: "The folder setup review expired; review the current card again"}
	case strings.Contains(message, "warnings must be acknowledged"):
		return &uicontrol.ProtocolError{Code: "warning-required", Message: "Acknowledge the folder safety warnings before creating it"}
	default:
		return &uicontrol.ProtocolError{Code: "operation-failed", Message: "The folder request could not be completed safely"}
	}
}

func controlOnboardingStatus(plan onboardingPlan) *uicontrol.OnboardingStatus {
	available := int64(plan.AvailableBytes)
	if plan.AvailableBytes > uint64(^uint64(0)>>1) {
		available = int64(^uint64(0) >> 1)
	}
	return &uicontrol.OnboardingStatus{
		PlanID: plan.ID, SourceID: plan.SourceID, CardID: plan.CardID, Kind: plan.Kind,
		FolderType: plan.FolderType, FolderID: plan.FolderID, Label: plan.Label, Path: plan.Path,
		FileCount: plan.FileCount, DirectoryCount: plan.DirectoryCount, ContentBytes: plan.ContentBytes,
		AvailableBytes: available, SnapshotPossible: plan.SnapshotPossible, PeerCount: plan.PeerCount,
		StatesWarning: plan.StatesWarning, JoinExisting: plan.OfferDeviceID != "", OfferDeviceID: plan.OfferDeviceID,
		ExpiresAt: plan.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

func findConfiguredFolder(folders []syncthingconfig.ConfiguredFolder, folderID string) (syncthingconfig.ConfiguredFolder, int, bool) {
	for index, folder := range folders {
		if folder.ID == folderID {
			return folder, index, true
		}
	}
	return syncthingconfig.ConfiguredFolder{}, -1, false
}

func controlStatus(session *Session, game life1.GameState, inventory []cards.Card, folderState map[string]folderControlRecord) uicontrol.Status {
	status := uicontrol.Status{
		Controller: "running",
		Upstream: uicontrol.UpstreamStatus{
			State: "running", Version: session.Identity.UpstreamVersion, DeviceID: session.Identity.DeviceID,
		},
		Game:         uicontrol.GameStatus{Active: game.Active, LaunchID: game.LaunchID, SourceID: game.SourceID},
		Recovery:     uicontrol.RecoveryStatus{State: "ready", Changed: session.Recovery.Changed},
		Capabilities: []string{uicontrol.OperationGet, uicontrol.OperationEnrollCard},
	}
	return applyInventory(status, inventory, session.Folders, folderState)
}

func controlGatewayStatus(status gatewayserver.Status) uicontrol.GatewayStatus {
	converted := uicontrol.GatewayStatus{
		Open: status.Open, URL: status.URL, PIN: status.PIN, QRURL: status.QRURL,
		Fingerprint: status.Fingerprint, TrustedBrowsers: status.TrustedBrowsers, Pairing: status.Pairing,
	}
	if !status.OfferExpires.IsZero() {
		converted.OfferExpires = status.OfferExpires.UTC().Format(time.RFC3339)
	}
	if !status.ExtensionExpires.IsZero() {
		converted.ExtensionExpires = status.ExtensionExpires.UTC().Format(time.RFC3339)
	}
	return converted
}

func applyInventory(status uicontrol.Status, inventory []cards.Card, folders []syncthingconfig.ConfiguredFolder, folderState map[string]folderControlRecord) uicontrol.Status {
	status = applyCardInventory(status, inventory)
	rows, folderIssues := reconcileManagedFolders(folders, inventory, folderState)
	issues := make([]uicontrol.Issue, 0, len(status.Issues)+len(folderIssues))
	for _, issue := range status.Issues {
		if issue.Scope != "folder" {
			issues = append(issues, issue)
		}
	}
	status.Folders = rows
	status.Issues = append(issues, folderIssues...)
	return status
}

func applyCardInventory(status uicontrol.Status, inventory []cards.Card) uicontrol.Status {
	status.Cards = []uicontrol.CardStatus{}
	issues := make([]uicontrol.Issue, 0, len(status.Issues))
	for _, issue := range status.Issues {
		if issue.Scope != "card" {
			issues = append(issues, issue)
		}
	}
	status.Issues = issues
	for _, card := range inventory {
		identifier := card.Identity.ID
		if identifier == "" {
			identifier = "source:" + card.Source.ID
		}
		row := uicontrol.CardStatus{
			ID: identifier, SourceID: card.Source.ID, IDSuffix: identitySuffix(card.Identity.ID), Slot: sourceLabel(card.Source),
			Root: card.Source.Root, State: string(card.State), Enrolled: card.Identity.ID != "",
			Present: card.Present, Writable: card.Writable, DuplicateID: card.DuplicateID,
			RetainedBytes: card.RetainedBytes, Issues: []uicontrol.Issue{},
		}
		for _, issue := range card.Issues {
			converted := uicontrol.Issue{Code: issue.Code, Message: issue.Message, Scope: "card", SubjectID: identifier}
			row.Issues = append(row.Issues, converted)
			status.Issues = append(status.Issues, converted)
		}
		status.Cards = append(status.Cards, row)
	}
	return status
}

func cardOperationError(err error) *uicontrol.ProtocolError {
	switch {
	case errors.Is(err, cards.ErrUnavailable):
		return &uicontrol.ProtocolError{Code: "card-absent", Message: "The requested card is not mounted"}
	case errors.Is(err, cards.ErrReadOnly):
		return &uicontrol.ProtocolError{Code: "card-read-only", Message: "The requested card is read-only"}
	case errors.Is(err, cards.ErrInvalidIdentity):
		return &uicontrol.ProtocolError{Code: "invalid-card-id", Message: "The existing card identity is invalid and was not replaced"}
	default:
		return &uicontrol.ProtocolError{Code: "operation-failed", Message: "Card enrollment failed without changing an existing identity"}
	}
}

func identitySuffix(identity string) string {
	if len(identity) <= 8 {
		return identity
	}
	return identity[len(identity)-8:]
}

func sourceLabel(source leaf.Source) string {
	if source.Primary || source.ID == "primary" {
		return "Primary"
	}
	if source.ID == "secondary_sd" {
		return "Secondary"
	}
	return source.ID
}

func reconcileGameState(current life1.GameState, event life1.Event) life1.GameState {
	switch event.Name {
	case "game.start":
		return life1.GameState{
			Active: true, LaunchID: event.LaunchID, SourceID: event.SourceID,
			SavesPath: event.SavesPath, StatesPath: event.StatesPath,
		}
	case "game.finish":
		if current.LaunchID == event.LaunchID {
			return life1.GameState{}
		}
	}
	return current
}

func shutdownUpstream(upstream UpstreamProcess) error {
	if upstream == nil {
		return nil
	}
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), StopGrace)
	defer shutdownCancel()
	if err := upstream.Shutdown(shutdownContext); err != nil {
		return fmt.Errorf("stop upstream: %w", err)
	}
	return nil
}

func handleLifecycleEvent(lifecycle Lifecycle, event life1.Event) error {
	switch event.Name {
	case "game.start":
		// Cooperative pause did not qualify in B0b. An unexpected notify
		// subscription must force Jawaka's verified-stop fallback, never ready.
		if err := lifecycle.SendError(event.LaunchID, "pause-unavailable"); err != nil {
			return fmt.Errorf("reject game.start: %w", err)
		}
	case "game.cancel", "game.finish":
		// Mode stop receives no game events. Retain the protocol no-op so a
		// runtime policy override cannot turn an ignorable finish into failure.
	default:
		return fmt.Errorf("unsupported LIFE-1 event %q", event.Name)
	}
	return nil
}
