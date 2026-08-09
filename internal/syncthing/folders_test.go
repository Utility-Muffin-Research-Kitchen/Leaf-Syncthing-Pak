package syncthing

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadManagedFoldersRecognizesOnlyStrictLeafBindings(t *testing.T) {
	directory := t.TempDir()
	config := `<configuration version="52">
<folder id="leaf-saves-0011223344556677" label="Leaf Saves" path="/card/Saves" type="sendonly"><paused>false</paused><markerName>.leaf-saves-aabbccddeeff</markerName><versioning type="simple"><fsPath>/card/.userdata/mlp1/Syncthing/versions/saves</fsPath><fsType>basic</fsType></versioning></folder>
<folder id="foreign" path="/elsewhere"></folder>
</configuration>`
	if err := os.WriteFile(filepath.Join(directory, "config.xml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	folders, err := ReadManagedFolders(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 1 || folders[0].Kind != "saves" || folders[0].Paused || folders[0].MarkerName != ".leaf-saves-aabbccddeeff" || folders[0].VersioningFSType != "basic" {
		t.Fatalf("managed folders = %+v", folders)
	}

	invalid := `<configuration version="52"><folder id="leaf-saves-not-a-binding" path="/card/Saves"></folder></configuration>`
	if err := os.WriteFile(filepath.Join(directory, "config.xml"), []byte(invalid), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManagedFolders(directory); err == nil {
		t.Fatal("malformed Leaf-owned folder id was ignored")
	}
}

func TestReadManagedFoldersForBindingsAcceptsStandardFolderID(t *testing.T) {
	directory := t.TempDir()
	config := `<configuration version="52">
<folder id="retro-saves" label="Retro Saves" path="/card/Saves" type="sendreceive"><paused>true</paused><markerName>.leaf-saves-aabbccddeeff</markerName><device id="AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"></device></folder>
<folder id="leaf-saves-not-registered" path="/elsewhere"></folder>
</configuration>`
	if err := os.WriteFile(filepath.Join(directory, "config.xml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	folders, err := ReadManagedFoldersForBindings(directory, map[string]string{"retro-saves": "saves"})
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 1 || folders[0].ID != "retro-saves" || folders[0].Kind != "saves" || !folders[0].Paused {
		t.Fatalf("bound folders = %+v", folders)
	}
	if _, err := ReadManagedFoldersForBindings(directory, map[string]string{"missing": "saves"}); err == nil {
		t.Fatal("missing registered folder was accepted")
	}
	folders, err = ReadManagedFoldersForBindings(directory, nil)
	if err != nil || len(folders) != 0 {
		t.Fatalf("nil binding registry selected folders: %+v, %v", folders, err)
	}
}

func TestValidFolderIDBoundsRESTPathInput(t *testing.T) {
	for _, value := range []string{"default", "abcde-fghij", "Leaf.Saves_2:hub"} {
		if !ValidFolderID(value) {
			t.Fatalf("valid folder id rejected: %q", value)
		}
	}
	for _, value := range []string{"", "slash/id", "space id", string(make([]byte, 65))} {
		if ValidFolderID(value) {
			t.Fatalf("invalid folder id accepted: %q", value)
		}
	}
}
