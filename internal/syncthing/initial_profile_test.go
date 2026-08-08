package syncthing

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyInitialProfilePreservesUnknownXML(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.xml")
	input := `<?xml version="1.0"?><configuration version="52" future="kept">
<!--keep-comment--><device id="` + testDeviceID + `"><future-device value="kept"/></device>
<gui enabled="true"><address>127.0.0.1:8384</address><apikey>secret</apikey><future-gui>kept</future-gui></gui>
<options><globalAnnounceEnabled>true</globalAnnounceEnabled><localAnnounceEnabled>false</localAnnounceEnabled><relaysEnabled>true</relaysEnabled><natEnabled>true</natEnabled><urAccepted>0</urAccepted><autoUpgradeIntervalH>12</autoUpgradeIntervalH><startBrowser>true</startBrowser><crashReportingEnabled>true</crashReportingEnabled><future-option attr="kept">value</future-option></options>
<future-root attr="kept">value</future-root></configuration>`
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(directory, "runtime", "syncthing-gui.sock")
	if err := ApplyInitialProfile(path, socket); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, fragment := range []string{
		`future="kept"`, `<!--keep-comment-->`, `<future-device value="kept"></future-device>`,
		`<future-gui>kept</future-gui>`, `<future-option attr="kept">value</future-option>`,
		`<future-root attr="kept">value</future-root>`, `<apikey>secret</apikey>`,
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("rewritten config dropped %q:\n%s", fragment, text)
		}
	}
	configuration, err := readConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.GUI.Address != socket || configuration.GUI.UnixPermissions != "0600" {
		t.Fatalf("GUI = %+v", configuration.GUI)
	}
	if configuration.Options.GlobalAnnounce || !configuration.Options.LocalAnnounce || configuration.Options.Relays ||
		configuration.Options.NAT || configuration.Options.URAccepted != -1 || configuration.Options.AutoUpgradeHours != 0 ||
		configuration.Options.StartBrowser || configuration.Options.CrashReporting {
		t.Fatalf("options = %+v", configuration.Options)
	}
	var generic any
	if err := xml.Unmarshal(contents, &generic); err != nil {
		t.Fatal(err)
	}
}

func TestApplyInitialProfileAddsMissingScalars(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.xml")
	input := `<configuration version="52"><gui enabled="true"><apikey>secret</apikey></gui><options></options></configuration>`
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ApplyInitialProfile(path, "/tmp/runtime/syncthing-gui.sock"); err != nil {
		t.Fatal(err)
	}
	configuration, err := readConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.GUI.Address == "" || configuration.GUI.UnixPermissions != "0600" || configuration.Options.URAccepted != -1 {
		t.Fatalf("profile was not inserted: %+v %+v", configuration.GUI, configuration.Options)
	}
}

func TestApplyInitialProfileRejectsNestedManagedScalar(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.xml")
	input := `<configuration version="52"><gui enabled="true"><address><nested/></address><apikey>secret</apikey></gui><options></options></configuration>`
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ApplyInitialProfile(path, "/tmp/runtime/syncthing-gui.sock"); err == nil {
		t.Fatal("nested managed scalar was accepted")
	}
}
