package cards

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/leaf"
)

func TestMLP1TwoCardSafety(t *testing.T) {
	if os.Getenv("LEAF_SYNCTHING_DEVICE_CARD_TEST") != "1" {
		t.Skip("set LEAF_SYNCTHING_DEVICE_CARD_TEST=1 on an isolated MLP1 fixture")
	}
	mountA := os.Getenv("LEAF_SYNCTHING_CARD_MOUNT_A")
	mountB := os.Getenv("LEAF_SYNCTHING_CARD_MOUNT_B")
	userdataA := os.Getenv("LEAF_SYNCTHING_CARD_USERDATA_A")
	userdataB := os.Getenv("LEAF_SYNCTHING_CARD_USERDATA_B")
	validateDeviceTestRoot(t, mountA, userdataA)
	validateDeviceTestRoot(t, mountB, userdataB)
	if mountA == mountB || userdataA == userdataB {
		t.Fatal("device card test requires two distinct mounts")
	}
	for _, root := range []string{userdataA, userdataB} {
		if _, err := os.Lstat(root); !os.IsNotExist(err) {
			t.Fatalf("refuse to replace existing device test root %s: %v", root, err)
		}
	}
	owned := true
	t.Cleanup(func() {
		if !owned {
			return
		}
		for _, root := range []string{userdataA, userdataB} {
			if err := os.RemoveAll(root); err != nil {
				t.Errorf("remove device test root %s: %v", root, err)
			}
		}
		for _, mount := range []string{mountA, mountB} {
			if err := syncFilesystemAt(mount); err != nil {
				t.Errorf("flush device test cleanup on %s: %v", mount, err)
			}
		}
	})

	normal := leaf.SourceList{
		{ID: "primary", Root: mountA, Primary: true, UserdataPath: userdataA},
		{ID: "secondary_sd", Root: mountB, UserdataPath: userdataB},
	}
	identityA, created, err := Enroll(normal[0], Options{})
	if err != nil || !created {
		t.Fatalf("enroll card A: %+v created=%v error=%v", identityA, created, err)
	}
	identityB, created, err := Enroll(normal[1], Options{})
	if err != nil || !created || identityB.ID == identityA.ID {
		t.Fatalf("enroll card B: %+v created=%v error=%v", identityB, created, err)
	}
	live, err := InspectSources(normal, Options{})
	if err != nil || len(live) != 2 || live[0].State != StateEnrolled || live[1].State != StateEnrolled {
		t.Fatalf("inspect enrolled cards: %+v, %v", live, err)
	}
	registryDirectory := filepath.Join(userdataA, leaf.AppStateName, "leaf")
	if err := ensureDirectoryWithin(mountA, registryDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileRegistry(registryDirectory, normal, live, nil); err != nil {
		t.Fatal(err)
	}

	reversed := leaf.SourceList{
		{ID: "primary", Root: mountB, Primary: true, UserdataPath: userdataB},
		{ID: "secondary_sd", Root: mountA, UserdataPath: userdataA},
	}
	swapped, err := InspectSources(reversed, Options{})
	if err != nil || swapped[0].Identity.ID != identityB.ID || swapped[1].Identity.ID != identityA.ID {
		t.Fatalf("physical identities did not follow cards across slot reversal: %+v, %v", swapped, err)
	}

	identityBPath := filepath.Join(userdataB, leaf.AppStateName, IdentityFileName)
	if err := os.Remove(identityBPath); err != nil {
		t.Fatal(err)
	}
	if err := syncFilesystemAt(mountB); err != nil {
		t.Fatal(err)
	}
	replacement, created, err := Enroll(normal[1], Options{})
	if err != nil || !created || replacement.ID == identityB.ID {
		t.Fatalf("replacement enrollment = %+v created=%v error=%v", replacement, created, err)
	}
	live, err = InspectSources(normal, Options{})
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := ReconcileRegistry(registryDirectory, normal, live, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory) != 3 || !hasCard(inventory, identityB.ID, false) || !hasCard(inventory, replacement.ID, true) {
		t.Fatalf("replacement inventory = %+v", inventory)
	}

	identityAPath := filepath.Join(userdataA, leaf.AppStateName, IdentityFileName)
	contents, err := os.ReadFile(identityAPath)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(identityBPath, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bytes.NewReader(contents).WriteTo(file); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := syncFilesystemAt(mountB); err != nil {
		t.Fatal(err)
	}
	clones, err := InspectSources(normal, Options{})
	if err != nil || clones[0].State != StateDuplicate || clones[1].State != StateDuplicate ||
		!clones[0].DuplicateID || !clones[1].DuplicateID {
		t.Fatalf("cloned identities did not fail closed: %+v, %v", clones, err)
	}
}

func validateDeviceTestRoot(t *testing.T, mount, userdata string) {
	t.Helper()
	mount = filepath.Clean(mount)
	userdata = filepath.Clean(userdata)
	want := filepath.Join(mount, ".userdata", "mlp1-b1-card-smoke")
	if !filepath.IsAbs(mount) || userdata != want {
		t.Fatalf("unsafe device test root %q for mount %q; want %q", userdata, mount, want)
	}
}

func hasCard(cards []Card, identity string, present bool) bool {
	for _, card := range cards {
		if card.Identity.ID == identity && card.Present == present {
			return true
		}
	}
	return false
}
