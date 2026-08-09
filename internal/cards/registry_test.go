package cards

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/leaf"
)

func TestRegistryRetainsAbsentPhysicalCard(t *testing.T) {
	directory := t.TempDir()
	sources := leaf.SourceList{
		{ID: "primary", Root: "/card/primary", UserdataPath: "/card/primary/userdata"},
		{ID: "secondary_sd", Root: "/card/secondary", UserdataPath: "/card/secondary/userdata"},
	}
	live := []Card{
		{Source: sources[0], Identity: Identity{Version: 1, ID: "00112233445566778899aabbccddeeff"}, State: StateEnrolled, Present: true, RetainedBytes: 42},
		{Source: sources[1], State: StateAbsent},
	}
	inventory, err := ReconcileRegistry(directory, sources, live, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory) != 2 || inventory[0].Identity.ID == "" {
		t.Fatalf("initial inventory = %+v", inventory)
	}

	removed := []Card{{Source: sources[0], State: StateAbsent}, {Source: sources[1], State: StateAbsent}}
	inventory, err = ReconcileRegistry(directory, sources, removed, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if inventory[0].Identity.ID != live[0].Identity.ID || inventory[0].RetainedBytes != 42 || inventory[0].State != StateAbsent {
		t.Fatalf("absent identity was lost: %+v", inventory)
	}
}

func TestRegistryDoesNotRedirectReplacementCard(t *testing.T) {
	directory := t.TempDir()
	source := leaf.Source{ID: "primary", Root: "/card", UserdataPath: "/card/userdata"}
	oldID := "00112233445566778899aabbccddeeff"
	_, err := ReconcileRegistry(directory, leaf.SourceList{source}, []Card{{
		Source: source, Identity: Identity{Version: 1, ID: oldID}, State: StateEnrolled, Present: true,
	}}, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	replacement := Card{Source: source, State: StateUnenrolled, Present: true, Writable: true}
	inventory, err := ReconcileRegistry(directory, leaf.SourceList{source}, []Card{replacement}, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory) != 2 || inventory[0].State != StateUnenrolled || inventory[1].Identity.ID != oldID || inventory[1].Present {
		t.Fatalf("replacement was redirected to old identity: %+v", inventory)
	}
}

func TestRegistryRestoresKnownGoodBackup(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, RegistryFileName)
	backup := path + ".bak"
	registry := registryFile{Version: 1, Cards: []RegistryRecord{{
		ID: "00112233445566778899aabbccddeeff", LastSourceID: "primary", RetainedBytes: 7,
	}}}
	payload, _ := json.Marshal(registry)
	if err := os.WriteFile(path, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	recovered, err := recoverRegistry(directory, func(string) error { return nil })
	if err != nil || len(recovered.Cards) != 1 || recovered.Cards[0].RetainedBytes != 7 {
		t.Fatalf("recovered registry = %+v, %v", recovered, err)
	}
}

func TestRegistrySyncFailureRecoversPromotedFile(t *testing.T) {
	directory := t.TempDir()
	registry := registryFile{Version: 1, Cards: []RegistryRecord{{
		ID: "00112233445566778899aabbccddeeff", LastSourceID: "primary",
	}}}
	if err := writeRegistry(directory, registry, func(string) error { return errors.New("injected syncfs failure") }); err == nil {
		t.Fatal("sync failure was accepted")
	}
	recovered, err := recoverRegistry(directory, func(string) error { return nil })
	if err != nil || len(recovered.Cards) != 1 {
		t.Fatalf("promoted registry did not converge: %+v, %v", recovered, err)
	}
}

func TestRegistryDiscardsUncommittedInitialTemporary(t *testing.T) {
	directory := t.TempDir()
	temporary := filepath.Join(directory, RegistryFileName+".tmp")
	if err := os.WriteFile(temporary, []byte(`{"version":1,"cards":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := recoverRegistry(directory, func(string) error { return nil })
	if err != nil || len(registry.Cards) != 0 {
		t.Fatalf("initial recovery = %+v, %v", registry, err)
	}
	if _, err := os.Lstat(temporary); !os.IsNotExist(err) {
		t.Fatalf("initial temporary remained: %v", err)
	}
}
