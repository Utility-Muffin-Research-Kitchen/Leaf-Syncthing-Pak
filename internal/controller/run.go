package controller

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
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
	folderControls, err := newFolderControlStore(
		filepath.Join(runner.Config.UserdataPath, leaf.AppStateName, "leaf", folderControlStateName),
		session.Folders,
	)
	if err != nil {
		return fmt.Errorf("load folder control state: %w", err)
	}
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
	gameState := session.State
	var status atomic.Value
	initialStatus := controlStatus(session, gameState, cardInventory, folderControls.Snapshot())
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
		if deviceUI == nil || current.Upstream.State != "running" {
			return current
		}
		refreshContext, refreshCancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		live, refreshErr := deviceUI.ReadUIStatus(refreshContext, session.Folders, session.Identity.DeviceID)
		refreshCancel()
		if refreshErr != nil {
			current = applyLiveStatusError(current)
		} else {
			current = applyLiveStatus(current, live)
		}
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
			if storage, storageErr := storageInventory(inventory); storageErr == nil {
				updated.Storage = &storage
			}
			status.Store(updated)
			return updated, nil
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

func controlStatus(session *Session, game life1.GameState, inventory []cards.Card, folderState ...map[string]folderControlRecord) uicontrol.Status {
	status := uicontrol.Status{
		Controller: "running",
		Upstream: uicontrol.UpstreamStatus{
			State: "running", Version: session.Identity.UpstreamVersion, DeviceID: session.Identity.DeviceID,
		},
		Game:         uicontrol.GameStatus{Active: game.Active, LaunchID: game.LaunchID, SourceID: game.SourceID},
		Recovery:     uicontrol.RecoveryStatus{State: "ready", Changed: session.Recovery.Changed},
		Capabilities: []string{uicontrol.OperationGet, uicontrol.OperationEnrollCard},
	}
	return applyInventory(status, inventory, session.Folders, folderState...)
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

func applyInventory(status uicontrol.Status, inventory []cards.Card, folders []syncthingconfig.ConfiguredFolder, folderState ...map[string]folderControlRecord) uicontrol.Status {
	status = applyCardInventory(status, inventory)
	rows, folderIssues := reconcileManagedFolders(folders, inventory, folderState...)
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
			ID: identifier, IDSuffix: identitySuffix(card.Identity.ID), Slot: sourceLabel(card.Source),
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
