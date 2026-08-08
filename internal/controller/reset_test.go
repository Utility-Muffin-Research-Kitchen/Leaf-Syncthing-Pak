package controller

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/cards"
	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/leaf"
)

func TestFullResetFaultBoundariesAreOneWayAndPreserveLiveContent(t *testing.T) {
	boundaries := []string{"before-intent", "intent-temporary-synced", "intent-persisted", "roots-synced", "intent-cleared"}
	for index := 0; index < 9; index++ {
		boundaries = append(boundaries, fmt.Sprintf("root-removed-%d", index))
	}
	for _, boundary := range boundaries {
		t.Run(boundary, func(t *testing.T) {
			config, inventory, sentinels := resetFixture(t)
			plan, err := PrepareResetPlan(config, inventory, ResetActionFull)
			if err != nil {
				t.Fatal(err)
			}
			document, _, err := readResetDocument(resetPlanPath(config))
			if err != nil {
				t.Fatal(err)
			}
			if len(document.Roots) != 9 {
				t.Fatalf("fixture roots = %d, want 9", len(document.Roots))
			}
			injected := errors.New("injected reset crash")
			err = ExecuteResetPlan(config, plan.ActionID, ResetOptions{
				Inventory:      func() ([]cards.Card, error) { return inventory, nil },
				SyncFilesystem: func(string) error { return nil },
				Fault: func(got string) error {
					if got == boundary {
						return injected
					}
					return nil
				},
			})
			if !errors.Is(err, injected) {
				t.Fatalf("fault result = %v", err)
			}
			if boundary == "before-intent" || boundary == "intent-temporary-synced" {
				for _, root := range document.Roots {
					if _, err := os.Lstat(root.Path); err != nil {
						t.Fatalf("pre-intent root changed: %s: %v", root.Path, err)
					}
				}
			}
			_, recoveryErr := RecoverReset(config, ResetOptions{
				Inventory:      func() ([]cards.Card, error) { return inventory, nil },
				SyncFilesystem: func(string) error { return nil },
			})
			if recoveryErr != nil {
				t.Fatal(recoveryErr)
			}
			if boundary != "before-intent" && boundary != "intent-temporary-synced" {
				for _, root := range document.Roots {
					if _, err := os.Lstat(root.Path); !os.IsNotExist(err) {
						t.Fatalf("post-intent root remained: %s: %v", root.Path, err)
					}
				}
			}
			for _, sentinel := range sentinels {
				if payload, err := os.ReadFile(sentinel); err != nil || string(payload) != "keep" {
					t.Fatalf("live content changed: %s = %q, %v", sentinel, payload, err)
				}
			}
		})
	}
}

func TestFullResetRefusesAbsentCardAndAvailableRecordsRetainedRoots(t *testing.T) {
	config, inventory, _ := resetFixture(t)
	inventory[0].Present = false
	inventory[0].State = cards.StateAbsent
	if _, err := PrepareResetPlan(config, inventory, ResetActionFull); !errors.Is(err, ErrResetCardAbsent) {
		t.Fatalf("full reset absent card = %v", err)
	}
	plan, err := PrepareResetPlan(config, inventory, ResetActionAvailable)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Retained) != 2 || len(plan.Remove) != 7 {
		t.Fatalf("available plan = %+v", plan)
	}
}

func TestDurableIntentWaitsForItsExactPhysicalCard(t *testing.T) {
	config, inventory, _ := resetFixture(t)
	plan, err := PrepareResetPlan(config, inventory, ResetActionFull)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("after intent")
	if err := ExecuteResetPlan(config, plan.ActionID, ResetOptions{
		Inventory:      func() ([]cards.Card, error) { return inventory, nil },
		SyncFilesystem: func(string) error { return nil },
		Fault: func(boundary string) error {
			if boundary == "intent-persisted" {
				return injected
			}
			return nil
		},
	}); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	absent := append([]cards.Card(nil), inventory...)
	absent[0].Present = false
	absent[0].State = cards.StateAbsent
	changed, err := RecoverReset(config, ResetOptions{
		Inventory:      func() ([]cards.Card, error) { return absent, nil },
		SyncFilesystem: func(string) error { return nil },
	})
	if !changed || !errors.Is(err, ErrResetPending) {
		t.Fatalf("absent recovery = %v, %v", changed, err)
	}
	if _, err := os.Lstat(resetIntentPath(config)); err != nil {
		t.Fatalf("pending intent disappeared: %v", err)
	}
}

func TestResetDocumentCannotNameLiveSavesStatesOrRoms(t *testing.T) {
	config, _, _ := resetFixture(t)
	for _, live := range []string{config.Sources[0].SavesPath, config.Sources[0].StatesPath, config.Sources[0].RomsPath} {
		document := resetDocument{
			Schema: 1, ActionID: "00112233445566778899aabbccddeeff", Action: ResetActionFull,
			Roots:         []resetRoot{{Path: live, FilesystemRoot: config.Sources[0].Root, Description: "attack"}},
			RequiredCards: []resetCard{}, Retained: []string{},
		}
		if err := validateResetDocument(config, document); err == nil {
			t.Fatalf("live content accepted: %s", live)
		}
	}
}

func TestIndexResetRejectsSymlinkRootBeforeSealingPlan(t *testing.T) {
	config := testConfig(t)
	if err := os.MkdirAll(config.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(config.DataDir, "index-v2.0.0.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareResetPlan(config, nil, ResetActionIndex); err == nil {
		t.Fatal("symlinked index root was included in a reset plan")
	}
	if _, err := os.Lstat(target); err != nil {
		t.Fatalf("symlink target changed: %v", err)
	}
}

func resetFixture(t *testing.T) (Config, []cards.Card, []string) {
	t.Helper()
	config := testConfig(t)
	root := config.Sources[0].Root
	config.Sources[0].SavesPath = filepath.Join(root, "Saves")
	config.Sources[0].StatesPath = filepath.Join(root, "States")
	config.Sources[0].RomsPath = filepath.Join(root, "Roms")
	state := filepath.Join(config.UserdataPath, leaf.AppStateName)
	paths := []string{
		filepath.Join(config.ConfigDir, "config.xml"), filepath.Join(config.DataDir, "index-v2.0.0.db", "index.db"),
		filepath.Join(state, "backups", "config.xml"), filepath.Join(state, "leaf", "trusted-clients.json"),
		filepath.Join(state, "leaf", "gateway-cert.pem"), filepath.Join(state, "leaf", "gateway-key.pem"),
		filepath.Join(state, "leaf", folderControlStateName), filepath.Join(state, "snapshots", "saves", "one"),
		filepath.Join(state, "versions", "saves", "one"), filepath.Join(state, cards.IdentityFileName),
	}
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("state"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sentinels := []string{}
	for _, directory := range []string{config.Sources[0].SavesPath, config.Sources[0].StatesPath, config.Sources[0].RomsPath} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, "sentinel")
		if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		sentinels = append(sentinels, path)
	}
	identity := "00112233445566778899aabbccddeeff"
	inventory := []cards.Card{{
		Source: config.Sources[0], Identity: cards.Identity{Version: 1, ID: identity},
		State: cards.StateEnrolled, Present: true, Writable: true,
	}}
	return config, inventory, sentinels
}
