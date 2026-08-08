package controller

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/uicontrol"
)

func TestDiagnosticsExportRedactsCredentialsAndPeerIdentity(t *testing.T) {
	config := testConfig(t)
	secret := "SUPER-SECRET-API-KEY-TOKEN-PASSWORD"
	status := uicontrol.Status{
		Controller: "running", Upstream: uicontrol.UpstreamStatus{State: "running", Version: "v2.1.2", DeviceID: secret},
		Game: uicontrol.GameStatus{LaunchID: secret, SourceID: secret}, Recovery: uicontrol.RecoveryStatus{State: "ready", PlanID: secret},
		Gateway: &uicontrol.GatewayStatus{Open: true, URL: secret, PIN: secret, QRURL: secret, Fingerprint: secret, TrustedBrowsers: 1},
		Cards:   []uicontrol.CardStatus{{ID: secret, IDSuffix: "ddeeff", Slot: "Primary", State: "enrolled"}},
		Folders: []uicontrol.FolderStatus{{ID: secret, Label: secret, Path: secret, Kind: "saves", Type: "sendonly", State: "idle", Versioning: "none"}},
		Peers:   []uicontrol.PeerStatus{{ID: secret, Name: secret, Address: secret, State: "connected", Connection: "local"}},
		Issues:  []uicontrol.Issue{{Code: "safe-code", Message: secret, SubjectID: secret}},
	}
	exported, err := exportDiagnostics(config, status, time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(exported.LastExportPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), secret) || !strings.Contains(string(payload), "safe-code") ||
		!strings.Contains(string(payload), `"connected": 1`) {
		t.Fatalf("diagnostics were not safely redacted: %s", payload)
	}
}
