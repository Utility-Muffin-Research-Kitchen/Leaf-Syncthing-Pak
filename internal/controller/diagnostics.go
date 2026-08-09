package controller

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/uicontrol"
)

const diagnosticsFileName = "leaf-syncthing-diagnostics.json"

type diagnosticReport struct {
	Schema      int                      `json:"schema"`
	ExportedAt  string                   `json:"exported_at"`
	Controller  string                   `json:"controller"`
	Upstream    diagnosticUpstream       `json:"upstream"`
	GameActive  bool                     `json:"game_active"`
	Recovery    string                   `json:"recovery"`
	Network     *uicontrol.NetworkStatus `json:"network,omitempty"`
	Gateway     diagnosticGateway        `json:"gateway"`
	Logging     *uicontrol.LoggingStatus `json:"logging,omitempty"`
	Storage     *uicontrol.StorageStatus `json:"storage,omitempty"`
	Cards       []diagnosticCard         `json:"cards"`
	Folders     []diagnosticFolder       `json:"folders"`
	PeerStates  map[string]int           `json:"peer_states"`
	Connections map[string]int           `json:"connection_kinds"`
	IssueCodes  []string                 `json:"issue_codes"`
}

type diagnosticUpstream struct {
	State   string `json:"state"`
	Version string `json:"version"`
}

type diagnosticGateway struct {
	Open            bool `json:"open"`
	TrustedBrowsers int  `json:"trusted_browsers"`
}

type diagnosticCard struct {
	IDSuffix      string `json:"id_suffix"`
	Slot          string `json:"slot"`
	State         string `json:"state"`
	Present       bool   `json:"present"`
	Writable      bool   `json:"writable"`
	RetainedBytes int64  `json:"retained_bytes"`
}

type diagnosticFolder struct {
	Kind          string   `json:"kind"`
	Type          string   `json:"type"`
	State         string   `json:"state"`
	Paused        bool     `json:"paused"`
	PauseReasons  []string `json:"pause_reasons"`
	PendingRescan bool     `json:"pending_rescan"`
	LocalBytes    int64    `json:"local_bytes"`
	GlobalBytes   int64    `json:"global_bytes"`
	PeerCount     int      `json:"peer_count"`
	Versioning    string   `json:"versioning"`
}

func exportDiagnostics(config Config, status uicontrol.Status, now time.Time) (uicontrol.DiagnosticsStatus, error) {
	report := diagnosticReport{
		Schema: 1, ExportedAt: now.UTC().Format(time.RFC3339), Controller: status.Controller,
		Upstream:   diagnosticUpstream{State: status.Upstream.State, Version: status.Upstream.Version},
		GameActive: status.Game.Active, Recovery: status.Recovery.State, Network: status.Network,
		Logging: status.Logging, Storage: status.Storage,
		Cards: []diagnosticCard{}, Folders: []diagnosticFolder{},
		PeerStates: map[string]int{}, Connections: map[string]int{}, IssueCodes: []string{},
	}
	if status.Gateway != nil {
		report.Gateway = diagnosticGateway{Open: status.Gateway.Open, TrustedBrowsers: status.Gateway.TrustedBrowsers}
	}
	for _, card := range status.Cards {
		report.Cards = append(report.Cards, diagnosticCard{
			IDSuffix: card.IDSuffix, Slot: card.Slot, State: card.State, Present: card.Present,
			Writable: card.Writable, RetainedBytes: card.RetainedBytes,
		})
	}
	for _, folder := range status.Folders {
		report.Folders = append(report.Folders, diagnosticFolder{
			Kind: folder.Kind, Type: folder.Type, State: folder.State, Paused: folder.Paused,
			PauseReasons: append([]string(nil), folder.PauseReasons...), PendingRescan: folder.PendingRescan,
			LocalBytes: folder.LocalBytes, GlobalBytes: folder.GlobalBytes,
			PeerCount: folder.PeerCount, Versioning: folder.Versioning,
		})
	}
	for _, peer := range status.Peers {
		report.PeerStates[peer.State]++
		report.Connections[peer.Connection]++
	}
	for _, issue := range status.Issues {
		report.IssueCodes = append(report.IssueCodes, issue.Code)
	}
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return uicontrol.DiagnosticsStatus{}, err
	}
	payload = append(payload, '\n')
	if err := os.MkdirAll(config.LogsPath, 0o700); err != nil {
		return uicontrol.DiagnosticsStatus{}, err
	}
	info, err := os.Lstat(config.LogsPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return uicontrol.DiagnosticsStatus{}, errors.New("diagnostics log directory is unsafe")
	}
	path := filepath.Join(config.LogsPath, diagnosticsFileName)
	temporary := path + ".tmp"
	if err := removeSafeRegularIfExists(temporary); err != nil {
		return uicontrol.DiagnosticsStatus{}, err
	}
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return uicontrol.DiagnosticsStatus{}, err
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return uicontrol.DiagnosticsStatus{}, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return uicontrol.DiagnosticsStatus{}, err
	}
	if err := file.Close(); err != nil {
		return uicontrol.DiagnosticsStatus{}, err
	}
	if err := os.Rename(temporary, path); err != nil {
		return uicontrol.DiagnosticsStatus{}, err
	}
	if err := syncStateFilesystem(config.LogsPath); err != nil {
		return uicontrol.DiagnosticsStatus{}, err
	}
	return uicontrol.DiagnosticsStatus{LastExportPath: path, LastExported: report.ExportedAt}, nil
}
