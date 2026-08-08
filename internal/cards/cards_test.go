package cards

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Utility-Muffin-Research-Kitchen/Leaf-Syncthing-Pak/internal/leaf"
)

func TestEnrollIsDurableAndIdempotent(t *testing.T) {
	source := testSource(t, "primary")
	syncCalls := 0
	options := Options{
		MountInfo: mountInfo(source.Root, "rw"), Random: bytes.NewReader(make([]byte, 16)),
		SyncFilesystem: func(path string) error {
			if path != source.Root {
				t.Fatalf("sync path = %s", path)
			}
			syncCalls++
			return nil
		},
	}
	identity, created, err := Enroll(source, options)
	if err != nil {
		t.Fatal(err)
	}
	if !created || identity.Version != 1 || identity.ID != "00000000000000000000000000000000" || syncCalls != 1 {
		t.Fatalf("enrollment = %+v created=%v syncs=%d", identity, created, syncCalls)
	}
	identityPath := filepath.Join(source.UserdataPath, leaf.AppStateName, IdentityFileName)
	first, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	again, created, err := Enroll(source, options)
	if err != nil || created || again != identity {
		t.Fatalf("second enrollment = %+v created=%v error=%v", again, created, err)
	}
	second, _ := os.ReadFile(identityPath)
	if !bytes.Equal(first, second) {
		t.Fatal("existing card identity was replaced")
	}
}

func TestInspectRecoversTemporaryAndDetectsDuplicate(t *testing.T) {
	first := testSource(t, "primary")
	second := testSource(t, "secondary_sd")
	identity := []byte(`{"version":1,"id":"00112233445566778899aabbccddeeff"}` + "\n")
	for _, source := range []leaf.Source{first, second} {
		root := filepath.Join(source.UserdataPath, leaf.AppStateName)
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, IdentityFileName), identity, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	temporary := filepath.Join(first.UserdataPath, leaf.AppStateName, TemporaryName)
	if err := os.WriteFile(temporary, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	cards, err := InspectSources(leaf.SourceList{first, second}, Options{
		MountInfo:      append(mountInfo(first.Root, "rw"), mountInfo(second.Root, "rw")...),
		SyncFilesystem: func(string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(temporary); !os.IsNotExist(err) {
		t.Fatalf("temporary remained: %v", err)
	}
	for _, card := range cards {
		if card.State != StateDuplicate || !card.DuplicateID {
			t.Fatalf("duplicate card = %+v", card)
		}
	}
}

func TestEnrollmentSyncFailureConvergesWithoutRegeneration(t *testing.T) {
	source := testSource(t, "primary")
	failing := Options{
		MountInfo: mountInfo(source.Root, "rw"), Random: bytes.NewReader(bytes.Repeat([]byte{0x5a}, 16)),
		SyncFilesystem: func(string) error { return errors.New("injected syncfs failure") },
	}
	if _, _, err := Enroll(source, failing); err == nil {
		t.Fatal("syncfs failure was accepted")
	}
	identity, created, err := Enroll(source, Options{
		MountInfo: mountInfo(source.Root, "rw"), Random: bytes.NewReader(bytes.Repeat([]byte{0xff}, 16)),
		SyncFilesystem: func(string) error { return nil },
	})
	if err != nil || created || identity.ID != strings.Repeat("5a", 16) {
		t.Fatalf("recovery = %+v created=%v error=%v", identity, created, err)
	}
}

func TestEnrollmentRefusesAbsentReadOnlyAndInvalidIdentity(t *testing.T) {
	source := testSource(t, "primary")
	if _, _, err := Enroll(source, Options{MountInfo: []byte{}}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("absent card error = %v", err)
	}
	if _, _, err := Enroll(source, Options{MountInfo: mountInfo(source.Root, "ro")}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only card error = %v", err)
	}
	root := filepath.Join(source.UserdataPath, leaf.AppStateName)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, IdentityFileName)
	if err := os.WriteFile(path, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Enroll(source, Options{MountInfo: mountInfo(source.Root, "rw")}); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("invalid identity error = %v", err)
	}
	contents, _ := os.ReadFile(path)
	if string(contents) != "broken" {
		t.Fatal("invalid identity was replaced")
	}
}

func TestInspectDoesNotFollowRetainedSymlink(t *testing.T) {
	source := testSource(t, "primary")
	root := filepath.Join(source.UserdataPath, leaf.AppStateName)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	cards, err := InspectSources(leaf.SourceList{source}, Options{
		MountInfo: mountInfo(source.Root, "rw"), SyncFilesystem: func(string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if cards[0].State != StateInvalid || len(cards[0].Issues) == 0 || cards[0].RetainedBytes != 0 {
		t.Fatalf("symlinked retained state = %+v", cards[0])
	}
}

func TestBindingNamesAndMarkerGuard(t *testing.T) {
	cardID := "00112233445566778899aabbccddeeff"
	folderID, marker, err := BindingNames(cardID, "saves")
	if err != nil || !strings.HasPrefix(folderID, "leaf-saves-") || !strings.HasPrefix(marker, ".leaf-saves-") ||
		len(marker) > 32 || marker == ".stfolder" {
		t.Fatalf("binding names = %q %q, %v", folderID, marker, err)
	}
	root := t.TempDir()
	if err := ValidateManagedMarker(root, marker); !errors.Is(err, ErrMarkerMissing) {
		t.Fatalf("missing marker error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, marker), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ValidateManagedMarker(root, marker); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".stfolder"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ValidateManagedMarker(root, marker); !errors.Is(err, ErrForeignMarker) {
		t.Fatalf("foreign marker error = %v", err)
	}
}

func testSource(t *testing.T, id string) leaf.Source {
	t.Helper()
	root := t.TempDir()
	return leaf.Source{ID: id, Root: root, Primary: id == "primary", UserdataPath: filepath.Join(root, ".userdata", "mlp1")}
}

func mountInfo(root, options string) []byte {
	escaped := strings.NewReplacer(" ", `\040`, "\t", `\011`, "\n", `\012`, `\`, `\134`).Replace(root)
	return []byte("33 18 179:97 / " + escaped + " " + options + " - vfat /dev/mmcblk1p1 rw\n")
}
