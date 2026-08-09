// Package uicontrol implements the package-private protocol between the
// resident Go controller and the foreground C/Catastrophe UI.
package uicontrol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const (
	Version                         = 1
	OperationGet                    = "status.get"
	OperationEnrollCard             = "card.enroll"
	OperationNetworkSet             = "network.profile.set"
	OperationGatewayOpen            = "gateway.open"
	OperationGatewayKeepAlive       = "gateway.keepalive"
	OperationGatewayClose           = "gateway.close"
	OperationGatewayExtend          = "gateway.extend"
	OperationGatewayRevoke          = "gateway.revoke-all"
	OperationFolderPause            = "folder.pause"
	OperationFolderResume           = "folder.resume"
	OperationFolderRescan           = "folder.rescan"
	OperationFolderRename           = "folder.rename"
	OperationFolderInspect          = "folder.inspect"
	OperationFolderOnboardPlan      = "folder.onboard.plan"
	OperationFolderOfferPlan        = "folder.offer.plan"
	OperationFolderOnboardCreate    = "folder.onboard.create"
	OperationFolderFirstSyncPrepare = "folder.first-sync.prepare"
	OperationFolderFirstSyncStart   = "folder.first-sync.start"
	OperationFolderTypeSet          = "folder.type.set"
	OperationDeviceAdd              = "device.add"
	OperationDeviceRename           = "device.rename"
	OperationResetPrepare           = "reset.prepare"
	OperationLogLevelSet            = "log.level.set"
	OperationDiagnosticsExport      = "diagnostics.export"
	MaxIdentifier                   = 64
)

type Request struct {
	Version   int             `json:"v"`
	ID        string          `json:"id"`
	Operation string          `json:"op"`
	Arguments json.RawMessage `json:"args"`
}

type ProtocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Response struct {
	Version int            `json:"v"`
	ID      string         `json:"id"`
	OK      bool           `json:"ok"`
	Result  *Status        `json:"result,omitempty"`
	Error   *ProtocolError `json:"error,omitempty"`
}

type Status struct {
	Controller   string              `json:"controller"`
	Upstream     UpstreamStatus      `json:"upstream"`
	Game         GameStatus          `json:"game"`
	Recovery     RecoveryStatus      `json:"recovery"`
	Network      *NetworkStatus      `json:"network,omitempty"`
	Gateway      *GatewayStatus      `json:"gateway,omitempty"`
	Transfer     *TransferStatus     `json:"transfer,omitempty"`
	Logging      *LoggingStatus      `json:"logging,omitempty"`
	Storage      *StorageStatus      `json:"storage,omitempty"`
	Diagnostics  *DiagnosticsStatus  `json:"diagnostics,omitempty"`
	Onboarding   *OnboardingStatus   `json:"onboarding,omitempty"`
	Cards        []CardStatus        `json:"cards"`
	Folders      []FolderStatus      `json:"folders"`
	Peers        []PeerStatus        `json:"peers,omitempty"`
	FolderOffers []FolderOfferStatus `json:"folder_offers,omitempty"`
	Issues       []Issue             `json:"issues"`
	Capabilities []string            `json:"capabilities"`
}

type NetworkStatus struct {
	Profile         string   `json:"profile"`
	AllowedNetworks []string `json:"allowed_networks"`
	RouteChanged    bool     `json:"route_changed"`
}

type GatewayStatus struct {
	Open             bool   `json:"open"`
	URL              string `json:"url"`
	PIN              string `json:"pin"`
	QRURL            string `json:"qr_url"`
	OfferExpires     string `json:"offer_expires"`
	Fingerprint      string `json:"fingerprint"`
	TrustedBrowsers  int    `json:"trusted_browsers"`
	Pairing          bool   `json:"pairing"`
	ExtensionExpires string `json:"extension_expires"`
}

type TransferStatus struct {
	State       string `json:"state"`
	LocalBytes  int64  `json:"local_bytes"`
	GlobalBytes int64  `json:"global_bytes"`
	NeedBytes   int64  `json:"need_bytes"`
	InBytes     int64  `json:"in_bytes"`
	OutBytes    int64  `json:"out_bytes"`
}

type LoggingStatus struct {
	Level        string `json:"level"`
	DebugExpires string `json:"debug_expires"`
}

type StorageStatus struct {
	SnapshotBytes int64            `json:"snapshot_bytes"`
	VersionBytes  int64            `json:"version_bytes"`
	SnapshotCount int              `json:"snapshot_count"`
	VersionGroups int              `json:"version_groups"`
	Inventory     []SnapshotStatus `json:"inventory"`
}

type SnapshotStatus struct {
	CardSuffix string `json:"card_suffix"`
	Category   string `json:"category"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Bytes      int64  `json:"bytes"`
}

type DiagnosticsStatus struct {
	LastExportPath string `json:"last_export_path"`
	LastExported   string `json:"last_exported"`
}

type OnboardingStatus struct {
	PlanID           string `json:"plan_id"`
	SourceID         string `json:"source_id"`
	CardID           string `json:"card_id"`
	Kind             string `json:"kind"`
	FolderType       string `json:"folder_type"`
	FolderID         string `json:"folder_id"`
	Label            string `json:"label"`
	Path             string `json:"path"`
	FileCount        int    `json:"file_count"`
	DirectoryCount   int    `json:"directory_count"`
	ContentBytes     int64  `json:"content_bytes"`
	AvailableBytes   int64  `json:"available_bytes"`
	SnapshotPossible bool   `json:"snapshot_possible"`
	PeerCount        int    `json:"peer_count"`
	StatesWarning    bool   `json:"states_warning"`
	JoinExisting     bool   `json:"join_existing,omitempty"`
	OfferDeviceID    string `json:"offer_device_id,omitempty"`
	ExpiresAt        string `json:"expires_at"`
}

type UpstreamStatus struct {
	State    string `json:"state"`
	Version  string `json:"version"`
	DeviceID string `json:"device_id"`
}

type GameStatus struct {
	Active   bool   `json:"active"`
	LaunchID string `json:"launch_id"`
	SourceID string `json:"source_id"`
}

type RecoveryStatus struct {
	State         string   `json:"state"`
	Changed       bool     `json:"changed"`
	PlanID        string   `json:"plan_id,omitempty"`
	PlanAction    string   `json:"plan_action,omitempty"`
	RemovePaths   []string `json:"remove_paths,omitempty"`
	RetainedPaths []string `json:"retained_paths,omitempty"`
}

type CardStatus struct {
	ID            string  `json:"id"`
	SourceID      string  `json:"source_id,omitempty"`
	IDSuffix      string  `json:"id_suffix"`
	Slot          string  `json:"slot"`
	Root          string  `json:"root"`
	State         string  `json:"state"`
	Enrolled      bool    `json:"enrolled"`
	Present       bool    `json:"present"`
	Writable      bool    `json:"writable"`
	DuplicateID   bool    `json:"duplicate_id"`
	RetainedBytes int64   `json:"retained_bytes"`
	Issues        []Issue `json:"issues"`
}

type FolderStatus struct {
	ID                  string   `json:"id"`
	Label               string   `json:"label"`
	CardID              string   `json:"card_id"`
	Kind                string   `json:"kind"`
	Path                string   `json:"path"`
	Type                string   `json:"type"`
	State               string   `json:"state"`
	Paused              bool     `json:"paused"`
	PauseReasons        []string `json:"pause_reasons"`
	PendingRescan       bool     `json:"pending_rescan"`
	LocalBytes          int64    `json:"local_bytes"`
	GlobalBytes         int64    `json:"global_bytes"`
	LocalItems          int      `json:"local_items,omitempty"`
	GlobalItems         int      `json:"global_items,omitempty"`
	PeerCount           int      `json:"peer_count"`
	LastSync            string   `json:"last_sync"`
	Versioning          string   `json:"versioning"`
	FirstSyncState      string   `json:"first_sync_state,omitempty"`
	SnapshotName        string   `json:"snapshot_name,omitempty"`
	SnapshotFiles       int      `json:"snapshot_files,omitempty"`
	SnapshotDirectories int      `json:"snapshot_directories,omitempty"`
	SnapshotBytes       int64    `json:"snapshot_bytes,omitempty"`
	FirstSyncMessage    string   `json:"first_sync_message,omitempty"`
	ConflictCount       int      `json:"conflict_count,omitempty"`
	Conflicts           []string `json:"conflicts,omitempty"`
	Issues              []Issue  `json:"issues"`
}

type PeerStatus struct {
	ID           string `json:"id"`
	IDSuffix     string `json:"id_suffix"`
	Name         string `json:"name"`
	State        string `json:"state"`
	Connection   string `json:"connection"`
	Address      string `json:"address"`
	Paused       bool   `json:"paused"`
	Introducer   bool   `json:"introducer"`
	IntroducedBy string `json:"introduced_by"`
	Pending      bool   `json:"pending"`
}

type FolderOfferStatus struct {
	FolderID         string `json:"folder_id"`
	Label            string `json:"label"`
	DeviceID         string `json:"device_id"`
	DeviceIDSuffix   string `json:"device_id_suffix"`
	DeviceName       string `json:"device_name"`
	OfferedAt        string `json:"offered_at"`
	ReceiveEncrypted bool   `json:"receive_encrypted"`
	RemoteEncrypted  bool   `json:"remote_encrypted"`
}

type Issue struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Scope     string `json:"scope"`
	SubjectID string `json:"subject_id"`
}

type Operations struct {
	Status            func() Status
	EnrollCard        func(string) (Status, *ProtocolError)
	SetNetworkProfile func(string) (Status, *ProtocolError)
	GatewayAction     func(string) (Status, *ProtocolError)
	FolderAction      func(string, string, string) (Status, *ProtocolError)
	FolderInspect     func(string) (Status, *ProtocolError)
	PlanFolder        func(string, string, string) (Status, *ProtocolError)
	PlanFolderOffer   func(string, string, string, string, string) (Status, *ProtocolError)
	CreateFolder      func(string, bool, bool) (Status, *ProtocolError)
	PrepareFirstSync  func(string) (Status, *ProtocolError)
	StartFirstSync    func(string, bool) (Status, *ProtocolError)
	SetFolderType     func(string, string) (Status, *ProtocolError)
	DeviceAction      func(string, string, string) (Status, *ProtocolError)
	PrepareReset      func(string) (Status, *ProtocolError)
	SetLogLevel       func(string) (Status, *ProtocolError)
	ExportDiagnostics func() (Status, *ProtocolError)
}

// Handle validates one request and returns one bounded response. Request
// objects are strict; response result objects are append-only so older UIs can
// ignore fields added by later package builds.
func Handle(payload json.RawMessage, status Status) Response {
	return (Operations{Status: func() Status { return status }}).Handle(payload)
}

func (operations Operations) Handle(payload json.RawMessage) Response {
	request, err := decodeRequest(payload)
	if err != nil {
		return failure("", "bad-request", "invalid UI control request")
	}
	responseID := ""
	if validIdentifier(request.ID) {
		responseID = request.ID
	}
	if request.Version != Version {
		return failure(responseID, "unsupported-version", "unsupported UI control protocol version")
	}
	if responseID == "" {
		return failure("", "bad-request", "invalid request id")
	}
	var status Status
	switch request.Operation {
	case OperationGet:
		if !emptyObject(request.Arguments) {
			return failure(responseID, "bad-arguments", "status.get requires empty args")
		}
		if operations.Status == nil {
			return failure(responseID, "internal", "controller status unavailable")
		}
		status = operations.Status()
	case OperationEnrollCard:
		if operations.EnrollCard == nil {
			return failure(responseID, "unsupported-op", "unsupported UI control operation")
		}
		sourceID, err := decodeEnrollCardArguments(request.Arguments)
		if err != nil {
			return failure(responseID, "bad-arguments", "card.enroll requires one valid source_id")
		}
		var operationError *ProtocolError
		status, operationError = operations.EnrollCard(sourceID)
		if operationError != nil {
			return Response{Version: Version, ID: responseID, OK: false, Error: operationError}
		}
	case OperationNetworkSet:
		if operations.SetNetworkProfile == nil {
			return failure(responseID, "unsupported-op", "unsupported UI control operation")
		}
		profile, err := decodeNetworkProfileArguments(request.Arguments)
		if err != nil {
			return failure(responseID, "bad-arguments", "network.profile.set requires a confirmed profile")
		}
		var operationError *ProtocolError
		status, operationError = operations.SetNetworkProfile(profile)
		if operationError != nil {
			return Response{Version: Version, ID: responseID, OK: false, Error: operationError}
		}
	case OperationGatewayOpen, OperationGatewayKeepAlive, OperationGatewayClose:
		if operations.GatewayAction == nil {
			return failure(responseID, "unsupported-op", "unsupported UI control operation")
		}
		if !emptyObject(request.Arguments) {
			return failure(responseID, "bad-arguments", "gateway operation requires empty args")
		}
		var operationError *ProtocolError
		status, operationError = operations.GatewayAction(request.Operation)
		if operationError != nil {
			return Response{Version: Version, ID: responseID, OK: false, Error: operationError}
		}
	case OperationGatewayExtend, OperationGatewayRevoke:
		if operations.GatewayAction == nil {
			return failure(responseID, "unsupported-op", "unsupported UI control operation")
		}
		if err := decodeConfirmedArguments(request.Arguments); err != nil {
			return failure(responseID, "bad-arguments", "gateway operation requires confirmation")
		}
		var operationError *ProtocolError
		status, operationError = operations.GatewayAction(request.Operation)
		if operationError != nil {
			return Response{Version: Version, ID: responseID, OK: false, Error: operationError}
		}
	case OperationFolderPause, OperationFolderResume, OperationFolderRescan:
		if operations.FolderAction == nil {
			return failure(responseID, "unsupported-op", "unsupported UI control operation")
		}
		folderID, err := decodeIDArguments(request.Arguments, "folder_id")
		if err != nil {
			return failure(responseID, "bad-arguments", "folder operation requires one valid folder_id")
		}
		var operationError *ProtocolError
		status, operationError = operations.FolderAction(request.Operation, folderID, "")
		if operationError != nil {
			return Response{Version: Version, ID: responseID, OK: false, Error: operationError}
		}
	case OperationFolderInspect:
		if operations.FolderInspect == nil {
			return failure(responseID, "unsupported-op", "unsupported UI control operation")
		}
		folderID, err := decodeIDArguments(request.Arguments, "folder_id")
		if err != nil {
			return failure(responseID, "bad-arguments", "folder.inspect requires a valid folder_id")
		}
		var operationError *ProtocolError
		status, operationError = operations.FolderInspect(folderID)
		if operationError != nil {
			return Response{Version: Version, ID: responseID, OK: false, Error: operationError}
		}
	case OperationFolderRename:
		if operations.FolderAction == nil {
			return failure(responseID, "unsupported-op", "unsupported UI control operation")
		}
		folderID, label, err := decodeNamedArguments(request.Arguments, "folder_id", "label", 96)
		if err != nil {
			return failure(responseID, "bad-arguments", "folder.rename requires a valid folder_id and label")
		}
		var operationError *ProtocolError
		status, operationError = operations.FolderAction(request.Operation, folderID, label)
		if operationError != nil {
			return Response{Version: Version, ID: responseID, OK: false, Error: operationError}
		}
	case OperationFolderOnboardPlan:
		if operations.PlanFolder == nil {
			return failure(responseID, "unsupported-op", "unsupported UI control operation")
		}
		sourceID, kind, folderType, err := decodeOnboardingPlanArguments(request.Arguments)
		if err != nil {
			return failure(responseID, "bad-arguments", "folder.onboard.plan requires a valid source, kind, and folder type")
		}
		var operationError *ProtocolError
		status, operationError = operations.PlanFolder(sourceID, kind, folderType)
		if operationError != nil {
			return Response{Version: Version, ID: responseID, OK: false, Error: operationError}
		}
	case OperationFolderOfferPlan:
		if operations.PlanFolderOffer == nil {
			return failure(responseID, "unsupported-op", "unsupported UI control operation")
		}
		folderID, deviceID, sourceID, kind, folderType, err := decodeFolderOfferPlanArguments(request.Arguments)
		if err != nil {
			return failure(responseID, "bad-arguments", "folder.offer.plan requires a valid offer, source, kind, and folder type")
		}
		var operationError *ProtocolError
		status, operationError = operations.PlanFolderOffer(folderID, deviceID, sourceID, kind, folderType)
		if operationError != nil {
			return Response{Version: Version, ID: responseID, OK: false, Error: operationError}
		}
	case OperationFolderOnboardCreate:
		if operations.CreateFolder == nil {
			return failure(responseID, "unsupported-op", "unsupported UI control operation")
		}
		planID, statesAcknowledged, manualAcknowledged, err := decodeOnboardingCreateArguments(request.Arguments)
		if err != nil {
			return failure(responseID, "bad-arguments", "folder.onboard.create requires confirmation and warning acknowledgments")
		}
		var operationError *ProtocolError
		status, operationError = operations.CreateFolder(planID, statesAcknowledged, manualAcknowledged)
		if operationError != nil {
			return Response{Version: Version, ID: responseID, OK: false, Error: operationError}
		}
	case OperationFolderFirstSyncPrepare:
		if operations.PrepareFirstSync == nil {
			return failure(responseID, "unsupported-op", "unsupported UI control operation")
		}
		folderID, err := decodeFirstSyncPrepareArguments(request.Arguments)
		if err != nil {
			return failure(responseID, "bad-arguments", "folder.first-sync.prepare requires confirmation of the snapshot limitation")
		}
		var operationError *ProtocolError
		status, operationError = operations.PrepareFirstSync(folderID)
		if operationError != nil {
			return Response{Version: Version, ID: responseID, OK: false, Error: operationError}
		}
	case OperationFolderFirstSyncStart:
		if operations.StartFirstSync == nil {
			return failure(responseID, "unsupported-op", "unsupported UI control operation")
		}
		folderID, hubAcknowledged, err := decodeFirstSyncStartArguments(request.Arguments)
		if err != nil {
			return failure(responseID, "bad-arguments", "folder.first-sync.start requires explicit confirmation and hub-versioning acknowledgment")
		}
		var operationError *ProtocolError
		status, operationError = operations.StartFirstSync(folderID, hubAcknowledged)
		if operationError != nil {
			return Response{Version: Version, ID: responseID, OK: false, Error: operationError}
		}
	case OperationFolderTypeSet:
		if operations.SetFolderType == nil {
			return failure(responseID, "unsupported-op", "unsupported UI control operation")
		}
		folderID, folderType, err := decodeFolderTypeArguments(request.Arguments)
		if err != nil {
			return failure(responseID, "bad-arguments", "folder.type.set requires a confirmed supported folder type")
		}
		var operationError *ProtocolError
		status, operationError = operations.SetFolderType(folderID, folderType)
		if operationError != nil {
			return Response{Version: Version, ID: responseID, OK: false, Error: operationError}
		}
	case OperationDeviceAdd, OperationDeviceRename:
		if operations.DeviceAction == nil {
			return failure(responseID, "unsupported-op", "unsupported UI control operation")
		}
		deviceID, name, err := decodeNamedArguments(request.Arguments, "device_id", "name", 64)
		if err != nil {
			return failure(responseID, "bad-arguments", "device operation requires a bounded device_id and name")
		}
		var operationError *ProtocolError
		status, operationError = operations.DeviceAction(request.Operation, deviceID, name)
		if operationError != nil {
			return Response{Version: Version, ID: responseID, OK: false, Error: operationError}
		}
	case OperationResetPrepare:
		if operations.PrepareReset == nil {
			return failure(responseID, "unsupported-op", "unsupported UI control operation")
		}
		action, err := decodeResetArguments(request.Arguments)
		if err != nil {
			return failure(responseID, "bad-arguments", "reset.prepare requires the exact strong confirmation")
		}
		var operationError *ProtocolError
		status, operationError = operations.PrepareReset(action)
		if operationError != nil {
			return Response{Version: Version, ID: responseID, OK: false, Error: operationError}
		}
	case OperationLogLevelSet:
		if operations.SetLogLevel == nil {
			return failure(responseID, "unsupported-op", "unsupported UI control operation")
		}
		level, err := decodeLogLevelArguments(request.Arguments)
		if err != nil {
			return failure(responseID, "bad-arguments", "log.level.set requires a confirmed supported level")
		}
		var operationError *ProtocolError
		status, operationError = operations.SetLogLevel(level)
		if operationError != nil {
			return Response{Version: Version, ID: responseID, OK: false, Error: operationError}
		}
	case OperationDiagnosticsExport:
		if operations.ExportDiagnostics == nil {
			return failure(responseID, "unsupported-op", "unsupported UI control operation")
		}
		if !emptyObject(request.Arguments) {
			return failure(responseID, "bad-arguments", "diagnostics.export requires empty args")
		}
		var operationError *ProtocolError
		status, operationError = operations.ExportDiagnostics()
		if operationError != nil {
			return Response{Version: Version, ID: responseID, OK: false, Error: operationError}
		}
	default:
		return failure(responseID, "unsupported-op", "unsupported UI control operation")
	}
	status.normalize()
	if err := status.validate(); err != nil {
		return failure(responseID, "internal", "controller status unavailable")
	}
	return Response{Version: Version, ID: responseID, OK: true, Result: &status}
}

func decodeConfirmedArguments(raw json.RawMessage) error {
	var arguments struct {
		Confirmed bool `json:"confirmed"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || !arguments.Confirmed {
		return errors.New("confirmation is required")
	}
	return nil
}

func decodeNetworkProfileArguments(raw json.RawMessage) (string, error) {
	var arguments struct {
		Profile   string `json:"profile"`
		Confirmed bool   `json:"confirmed"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return "", err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || !arguments.Confirmed ||
		(arguments.Profile != "lan-only" && arguments.Profile != "sync-anywhere") {
		return "", errors.New("invalid network profile arguments")
	}
	return arguments.Profile, nil
}

func decodeEnrollCardArguments(raw json.RawMessage) (string, error) {
	var arguments struct {
		SourceID string `json:"source_id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return "", err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || !validIdentifier(arguments.SourceID) {
		return "", errors.New("invalid card enrollment arguments")
	}
	return arguments.SourceID, nil
}

func decodeOnboardingPlanArguments(raw json.RawMessage) (string, string, string, error) {
	var arguments struct {
		SourceID   string `json:"source_id"`
		Kind       string `json:"kind"`
		FolderType string `json:"folder_type"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return "", "", "", err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || !validIdentifier(arguments.SourceID) ||
		(arguments.Kind != "saves" && arguments.Kind != "states") ||
		(arguments.FolderType != "sendonly" && arguments.FolderType != "sendreceive" && arguments.FolderType != "receiveonly") {
		return "", "", "", errors.New("invalid onboarding plan arguments")
	}
	return arguments.SourceID, arguments.Kind, arguments.FolderType, nil
}

func decodeFolderOfferPlanArguments(raw json.RawMessage) (string, string, string, string, string, error) {
	var arguments struct {
		FolderID   string `json:"folder_id"`
		DeviceID   string `json:"device_id"`
		SourceID   string `json:"source_id"`
		Kind       string `json:"kind"`
		FolderType string `json:"folder_type"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return "", "", "", "", "", err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || !validIdentifier(arguments.FolderID) ||
		!validIdentifier(arguments.DeviceID) || !validIdentifier(arguments.SourceID) ||
		(arguments.Kind != "saves" && arguments.Kind != "states") ||
		(arguments.FolderType != "sendonly" && arguments.FolderType != "sendreceive" && arguments.FolderType != "receiveonly") {
		return "", "", "", "", "", errors.New("invalid folder offer plan arguments")
	}
	return arguments.FolderID, arguments.DeviceID, arguments.SourceID, arguments.Kind, arguments.FolderType, nil
}

func decodeOnboardingCreateArguments(raw json.RawMessage) (string, bool, bool, error) {
	var arguments struct {
		PlanID                    string `json:"plan_id"`
		Confirmed                 bool   `json:"confirmed"`
		StatesWarningAcknowledged bool   `json:"states_warning_acknowledged"`
		ManualEditAcknowledged    bool   `json:"manual_edit_warning_acknowledged"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return "", false, false, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || len(arguments.PlanID) != 32 ||
		!validIdentifier(arguments.PlanID) || !arguments.Confirmed || !arguments.ManualEditAcknowledged {
		return "", false, false, errors.New("invalid onboarding creation arguments")
	}
	return arguments.PlanID, arguments.StatesWarningAcknowledged, arguments.ManualEditAcknowledged, nil
}

func decodeFirstSyncPrepareArguments(raw json.RawMessage) (string, error) {
	var arguments struct {
		FolderID                  string `json:"folder_id"`
		Confirmed                 bool   `json:"confirmed"`
		SnapshotLimitAcknowledged bool   `json:"snapshot_limit_acknowledged"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return "", err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || !validIdentifier(arguments.FolderID) ||
		!arguments.Confirmed || !arguments.SnapshotLimitAcknowledged {
		return "", errors.New("invalid first-sync prepare arguments")
	}
	return arguments.FolderID, nil
}

func decodeFirstSyncStartArguments(raw json.RawMessage) (string, bool, error) {
	var arguments struct {
		FolderID                  string `json:"folder_id"`
		Confirmed                 bool   `json:"confirmed"`
		HubVersioningAcknowledged bool   `json:"hub_versioning_acknowledged"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return "", false, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || !validIdentifier(arguments.FolderID) ||
		!arguments.Confirmed || !arguments.HubVersioningAcknowledged {
		return "", false, errors.New("invalid first-sync start arguments")
	}
	return arguments.FolderID, arguments.HubVersioningAcknowledged, nil
}

func decodeFolderTypeArguments(raw json.RawMessage) (string, string, error) {
	var arguments struct {
		FolderID   string `json:"folder_id"`
		FolderType string `json:"folder_type"`
		Confirmed  bool   `json:"confirmed"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return "", "", err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || !validIdentifier(arguments.FolderID) || !arguments.Confirmed ||
		(arguments.FolderType != "sendonly" && arguments.FolderType != "sendreceive" && arguments.FolderType != "receiveonly") {
		return "", "", errors.New("invalid folder type arguments")
	}
	return arguments.FolderID, arguments.FolderType, nil
}

func decodeIDArguments(raw json.RawMessage, field string) (string, error) {
	var arguments map[string]json.RawMessage
	if err := decodeStrictMap(raw, &arguments); err != nil || len(arguments) != 1 {
		return "", errors.New("invalid id arguments")
	}
	encoded, ok := arguments[field]
	if !ok {
		return "", errors.New("missing id")
	}
	var value string
	if err := json.Unmarshal(encoded, &value); err != nil || !validIdentifier(value) {
		return "", errors.New("invalid id")
	}
	return value, nil
}

func decodeNamedArguments(raw json.RawMessage, idField, nameField string, maxName int) (string, string, error) {
	var arguments map[string]json.RawMessage
	if err := decodeStrictMap(raw, &arguments); err != nil || len(arguments) != 2 {
		return "", "", errors.New("invalid named arguments")
	}
	var id, name string
	if encoded, ok := arguments[idField]; !ok || json.Unmarshal(encoded, &id) != nil || id == "" || len(id) > 256 {
		return "", "", errors.New("invalid subject id")
	}
	if encoded, ok := arguments[nameField]; !ok || json.Unmarshal(encoded, &name) != nil || !validDisplayText(name, maxName) {
		return "", "", errors.New("invalid display name")
	}
	return id, name, nil
}

func decodeResetArguments(raw json.RawMessage) (string, error) {
	var arguments struct {
		Action       string `json:"action"`
		Confirmed    bool   `json:"confirmed"`
		Confirmation string `json:"confirmation"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return "", err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || !arguments.Confirmed {
		return "", errors.New("reset confirmation is required")
	}
	want := map[string]string{
		"index-only": "RESET INDEX", "full": "RESET SYNCTHING", "available-only": "RESET AVAILABLE STATE",
	}[arguments.Action]
	if want == "" || arguments.Confirmation != want {
		return "", errors.New("reset confirmation phrase does not match")
	}
	return arguments.Action, nil
}

func decodeLogLevelArguments(raw json.RawMessage) (string, error) {
	var arguments struct {
		Level     string `json:"level"`
		Confirmed bool   `json:"confirmed"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return "", err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || !arguments.Confirmed ||
		(arguments.Level != "normal" && arguments.Level != "debug") {
		return "", errors.New("invalid log level")
	}
	return arguments.Level, nil
}

func decodeStrictMap(raw json.RawMessage, target *map[string]json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(target); err != nil || *target == nil {
		return errors.New("invalid object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func validDisplayText(value string, maximum int) bool {
	if len(value) == 0 || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func decodeRequest(payload json.RawMessage) (Request, error) {
	var request Request
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return Request{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Request{}, errors.New("request contains trailing JSON")
	}
	return request, nil
}

func emptyObject(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return false
	}
	return len(fields) == 0
}

func validIdentifier(value string) bool {
	if len(value) == 0 || len(value) > MaxIdentifier {
		return false
	}
	for _, character := range []byte(value) {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '.' || character == '_' ||
			character == ':' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func failure(id, code, message string) Response {
	return Response{
		Version: Version, ID: id, OK: false,
		Error: &ProtocolError{Code: code, Message: message},
	}
}

func (status *Status) normalize() {
	if status.Cards == nil {
		status.Cards = []CardStatus{}
	} else {
		status.Cards = append([]CardStatus{}, status.Cards...)
	}
	if status.Folders == nil {
		status.Folders = []FolderStatus{}
	} else {
		status.Folders = append([]FolderStatus{}, status.Folders...)
	}
	if status.Peers != nil {
		status.Peers = append([]PeerStatus(nil), status.Peers...)
	}
	if status.FolderOffers != nil {
		status.FolderOffers = append([]FolderOfferStatus(nil), status.FolderOffers...)
	}
	if status.Recovery.RemovePaths != nil {
		status.Recovery.RemovePaths = append([]string(nil), status.Recovery.RemovePaths...)
	}
	if status.Recovery.RetainedPaths != nil {
		status.Recovery.RetainedPaths = append([]string(nil), status.Recovery.RetainedPaths...)
	}
	if status.Issues == nil {
		status.Issues = []Issue{}
	}
	if status.Capabilities == nil {
		status.Capabilities = []string{}
	}
	if status.Network != nil {
		status.Network.AllowedNetworks = append([]string(nil), status.Network.AllowedNetworks...)
		if status.Network.AllowedNetworks == nil {
			status.Network.AllowedNetworks = []string{}
		}
	}
	for index := range status.Cards {
		if status.Cards[index].Issues == nil {
			status.Cards[index].Issues = []Issue{}
		}
	}
	for index := range status.Folders {
		if status.Folders[index].Conflicts != nil {
			status.Folders[index].Conflicts = append([]string(nil), status.Folders[index].Conflicts...)
		}
		if status.Folders[index].PauseReasons == nil {
			status.Folders[index].PauseReasons = []string{}
		}
		if status.Folders[index].Issues == nil {
			status.Folders[index].Issues = []Issue{}
		}
	}
}

func (status Status) validate() error {
	if !oneOf(status.Controller, "running", "recovery-pending", "error") ||
		!oneOf(status.Upstream.State, "stopped", "starting", "running", "error", "conflict") ||
		!oneOf(status.Recovery.State, "ready", "pending", "error") {
		return errors.New("invalid controller state")
	}
	if status.Network != nil && !oneOf(status.Network.Profile, "lan-only", "sync-anywhere") {
		return errors.New("invalid network profile")
	}
	if status.Gateway != nil {
		if status.Gateway.TrustedBrowsers < 0 || status.Gateway.TrustedBrowsers > 32 ||
			(status.Gateway.Open && (status.Gateway.URL == "" || status.Gateway.Fingerprint == "")) ||
			(status.Gateway.Pairing && (!status.Gateway.Open || len(status.Gateway.PIN) != 4 ||
				status.Gateway.QRURL == "" || status.Gateway.OfferExpires == "")) {
			return errors.New("invalid gateway status")
		}
	}
	if status.Transfer != nil && (status.Transfer.LocalBytes < 0 || status.Transfer.GlobalBytes < 0 ||
		status.Transfer.NeedBytes < 0 || status.Transfer.InBytes < 0 || status.Transfer.OutBytes < 0) {
		return errors.New("invalid transfer status")
	}
	if status.Logging != nil && !oneOf(status.Logging.Level, "normal", "debug") {
		return errors.New("invalid logging status")
	}
	if status.Onboarding != nil {
		plan := status.Onboarding
		if len(plan.PlanID) != 32 || !validIdentifier(plan.PlanID) || !validIdentifier(plan.SourceID) ||
			plan.CardID == "" || !validIdentifier(plan.FolderID) || !validDisplayText(plan.Label, 96) ||
			len(plan.Path) == 0 || len(plan.Path) > 1024 ||
			!oneOf(plan.Kind, "saves", "states") || !oneOf(plan.FolderType, "sendonly", "sendreceive", "receiveonly") ||
			plan.FileCount < 0 || plan.DirectoryCount < 0 || plan.ContentBytes < 0 || plan.AvailableBytes < 0 ||
			plan.PeerCount < 1 || plan.ExpiresAt == "" || len(plan.ExpiresAt) > 64 ||
			plan.JoinExisting != (plan.OfferDeviceID != "") || (plan.JoinExisting && !validIdentifier(plan.OfferDeviceID)) {
			return errors.New("invalid folder onboarding status")
		}
	}
	if status.Storage != nil {
		if status.Storage.SnapshotBytes < 0 || status.Storage.VersionBytes < 0 || status.Storage.SnapshotCount < 0 ||
			status.Storage.VersionGroups < 0 || len(status.Storage.Inventory) > 128 {
			return errors.New("invalid storage status")
		}
		for _, row := range status.Storage.Inventory {
			if row.CardSuffix == "" || row.Name == "" || row.Bytes < 0 ||
				!oneOf(row.Category, "snapshot", "versions") || !oneOf(row.Kind, "saves", "states", "other") {
				return errors.New("invalid storage inventory row")
			}
		}
	}
	if len(status.Recovery.RemovePaths) > 64 || len(status.Recovery.RetainedPaths) > 32 ||
		(status.Recovery.PlanID != "" && (len(status.Recovery.PlanID) != 32 ||
			!oneOf(status.Recovery.PlanAction, "index-only", "full", "available-only"))) {
		return errors.New("invalid reset recovery status")
	}
	if status.Upstream.State == "running" && (status.Upstream.Version == "" || status.Upstream.DeviceID == "") {
		return errors.New("incomplete running status")
	}
	if len(status.Cards) > 128 || len(status.Folders) > 128 || len(status.Peers) > 128 || len(status.FolderOffers) > 32 || len(status.Issues) > 128 || len(status.Capabilities) > 64 {
		return errors.New("status exceeds row limits")
	}
	for _, card := range status.Cards {
		if card.ID == "" || !oneOf(card.State, "absent", "unenrolled", "enrolled", "invalid", "duplicate") ||
			(card.SourceID != "" && !validIdentifier(card.SourceID)) || card.RetainedBytes < 0 || len(card.Issues) > 128 {
			return errors.New("card status is outside protocol bounds")
		}
	}
	for _, folder := range status.Folders {
		if folder.LocalBytes < 0 || folder.GlobalBytes < 0 || folder.LocalItems < 0 || folder.GlobalItems < 0 || folder.PeerCount < 0 ||
			folder.SnapshotFiles < 0 || folder.SnapshotDirectories < 0 || folder.SnapshotBytes < 0 ||
			folder.ConflictCount < 0 || folder.ConflictCount < len(folder.Conflicts) ||
			len(folder.Conflicts) > 64 || len(folder.PauseReasons) > 16 || len(folder.Issues) > 128 {
			return errors.New("folder status is outside protocol bounds")
		}
		if folder.FirstSyncState != "" && !oneOf(folder.FirstSyncState, "required", "preparing", "ready", "complete", "error") {
			return errors.New("folder first-sync state is outside protocol bounds")
		}
		if len(folder.SnapshotName) > 96 || len(folder.FirstSyncMessage) > 240 {
			return errors.New("folder first-sync text is outside protocol bounds")
		}
		for _, conflict := range folder.Conflicts {
			if !validDisplayText(conflict, 256) {
				return errors.New("folder conflict path is outside protocol bounds")
			}
		}
	}
	for _, peer := range status.Peers {
		if peer.ID == "" || peer.Name == "" || !oneOf(peer.State, "offline", "connected", "paused", "pending") ||
			!oneOf(peer.Connection, "none", "local", "direct", "relay") {
			return errors.New("peer status is outside protocol bounds")
		}
	}
	for _, offer := range status.FolderOffers {
		if !validIdentifier(offer.FolderID) || !validDisplayText(offer.Label, 96) ||
			offer.DeviceID == "" || offer.DeviceIDSuffix == "" || !validDisplayText(offer.DeviceName, 64) ||
			len(offer.OfferedAt) > 64 {
			return errors.New("folder offer is outside protocol bounds")
		}
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
