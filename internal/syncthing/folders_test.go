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
