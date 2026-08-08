package leaf

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func mapEnv(values map[string]string) getenvFunc {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func TestEnvironmentOneAndTwoCardSources(t *testing.T) {
	tests := []struct {
		name  string
		env   map[string]string
		roots []string
	}{
		{
			name: "one card",
			env: map[string]string{
				"PLATFORM": "mlp1", "SDCARD_PATH": "/cards/a", "SDCARD_PATHS": "/cards/a",
			},
			roots: []string{"/cards/a"},
		},
		{
			name: "two cards preserve declared order",
			env: map[string]string{
				"PLATFORM": "mlp1", "SDCARD_PATH": "/cards/b",
				"SDCARD_PATHS": "/cards/b/:/cards/a/", "UMRK_SECONDARY_SDCARD_PATH": "/cards/a",
				"MUSIC_PATHS": "/cards/b/Music:/cards/a/Music",
			},
			roots: []string{"/cards/b", "/cards/a"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env, err := loadEnvironment(mapEnv(test.env))
			if err != nil {
				t.Fatalf("loadEnvironment: %v", err)
			}
			if len(env.Sources) != len(test.roots) {
				t.Fatalf("source count = %d, want %d", len(env.Sources), len(test.roots))
			}
			for i, root := range test.roots {
				if env.Sources[i].Root != root {
					t.Errorf("source[%d].Root = %q, want %q", i, env.Sources[i].Root, root)
				}
			}
		})
	}
}

func TestEnvironmentRejectsMalformedSourceLists(t *testing.T) {
	tests := []map[string]string{
		{"PLATFORM": "mlp1", "SDCARD_PATH": "/a", "SDCARD_PATHS": "/a::/b"},
		{"PLATFORM": "mlp1", "SDCARD_PATH": "/a", "SDCARD_PATHS": "/a:/a/"},
		{"PLATFORM": "mlp1", "SDCARD_PATH": "/a", "SDCARD_PATHS": "/a:/b", "MUSIC_PATHS": "/a/Music"},
		{"PLATFORM": "mlp1", "SDCARD_PATH": "/a", "SDCARD_PATHS": "/a:/b", "MUSIC_PATH": "/elsewhere", "MUSIC_PATHS": "/a/Music:/b/Music"},
		{"PLATFORM": "mlp1", "SDCARD_PATH": "/a", "SDCARD_PATHS": "/b:/a"},
	}
	for i, values := range tests {
		if _, err := loadEnvironment(mapEnv(values)); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

func TestEnvironmentValidatesSourcePathsV2(t *testing.T) {
	values := map[string]string{
		"UMRK_ENV_VERSION": "2", "PLATFORM": "mlp1",
		"SDCARD_PATH": "/cards/a", "SDCARD_PATHS": "/cards/a:/cards/b",
		"USERDATA_PATH": "/cards/a/userdata", "USERDATA_PATHS": "/cards/a/userdata:/cards/b/userdata",
		"SHARED_USERDATA_PATH": "/cards/a/shared", "SHARED_USERDATA_PATHS": "/cards/a/shared:/cards/b/shared",
		"SAVES_PATH": "/cards/a/Saves", "SAVES_PATHS": "/cards/a/Saves:/cards/b/Saves",
		"STATES_PATH": "/cards/a/States", "STATES_PATHS": "/cards/a/States:/cards/b/States",
	}
	environment, err := loadEnvironment(mapEnv(values))
	if err != nil {
		t.Fatal(err)
	}
	if !environment.SourcePathsV2 || environment.Sources[1].UserdataPath != "/cards/b/userdata" ||
		environment.Sources[1].SharedUserdataPath != "/cards/b/shared" {
		t.Fatalf("source-paths-v2 was not retained: %+v", environment)
	}

	for name, value := range map[string]string{
		"USERDATA_PATHS":        "/cards/a/userdata",
		"SHARED_USERDATA_PATHS": "/cards/a/shared:/elsewhere/shared",
		"SAVES_PATH":            "/cards/a/Saves/",
	} {
		broken := make(map[string]string, len(values))
		for key, original := range values {
			broken[key] = original
		}
		broken[name] = value
		if _, err := loadEnvironment(mapEnv(broken)); err == nil {
			t.Errorf("accepted invalid source-paths-v2 %s=%q", name, value)
		}
	}
}

func TestMissingSecondaryRemainsIdentifiable(t *testing.T) {
	primary := t.TempDir()
	missing := filepath.Join(t.TempDir(), "removed-card")
	env, err := loadEnvironment(mapEnv(map[string]string{
		"PLATFORM": "mlp1", "SDCARD_PATH": primary,
		"SDCARD_PATHS":               primary + ":" + missing,
		"UMRK_SECONDARY_SDCARD_PATH": missing,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if env.Sources[1].ID != "secondary_sd" || env.Sources[1].Available() {
		t.Fatalf("secondary source = %#v, want identifiable and unavailable", env.Sources[1])
	}
}

func TestExistingMLP1MountpointDirectoryIsNotAvailableWithoutMount(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("MLP1 mount verification is Linux-specific")
	}
	source := Source{Root: t.TempDir(), MustBeMounted: true}
	if source.Available() {
		t.Fatal("plain directory was mistaken for a mounted removable source")
	}
}

func TestMountInfoHasRoot(t *testing.T) {
	mountInfo := []byte("33 18 179:97 / /mnt/sdcard rw - vfat /dev/mmcblk1p1 rw\n" +
		"34 18 179:98 / /media/card\\040two rw - vfat /dev/mmcblk2p1 rw\n")
	for _, root := range []string{"/mnt/sdcard", "/media/card two"} {
		if !mountInfoHasRoot(mountInfo, root) {
			t.Errorf("mount %q not detected", root)
		}
	}
	if mountInfoHasRoot(mountInfo, "/media/sdcard1") {
		t.Fatal("unmounted root was reported available")
	}
}

func TestPathGuardRejectsTraversal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Music")
	if _, err := JoinWithin(root, "Album", "track.flac"); err != nil {
		t.Fatalf("safe join: %v", err)
	}
	for _, part := range []string{"../escape", "/absolute"} {
		if _, err := JoinWithin(root, part); err == nil {
			t.Errorf("JoinWithin accepted %q", part)
		}
	}
	if _, err := RelativeWithin(root, filepath.Join(filepath.Dir(root), "escape")); err == nil {
		t.Error("RelativeWithin accepted an escaped target")
	}
}

func TestCatalogUsesCanonicalSourceLocalRoots(t *testing.T) {
	platform := t.TempDir()
	defaults := filepath.Join(platform, "defaults")
	if err := os.MkdirAll(defaults, 0o755); err != nil {
		t.Fatal(err)
	}
	json := `{"version":1,"platform":"mlp1","systems":[
		{"id":"GB","name":"Game Boy","rom_root":"Roms/GB","image_root":"Images/GB"},
		{"id":"GBC","name":"GBC","rom_root":"Roms/GBC","image_root":"Images/GBC"},
		{"id":"GBA","name":"GBA","rom_root":"Roms/GBA","image_root":"Images/GBA"},
		{"id":"FC","name":"NES","rom_root":"Roms/NES","image_root":"Images/NES"},
		{"id":"MD","name":"Genesis","rom_root":"Roms/GENESIS","image_root":"Images/GENESIS"},
		{"id":"PICO8","name":"Pico-8","rom_root":"Roms/PICO8","image_root":"Images/PICO8"},
		{"id":"PS","name":"PlayStation","rom_root":"Roms/PSX","image_root":"Images/PSX"}
	]}`
	if err := os.WriteFile(filepath.Join(defaults, "systems.json"), []byte(json), 0o644); err != nil {
		t.Fatal(err)
	}
	env := Environment{Platform: "mlp1", PlatformPath: platform}
	catalog, err := LoadCatalog(env)
	if err != nil {
		t.Fatal(err)
	}
	source := Source{RomsPath: "/secondary/Roms", ImagesPath: "/secondary/Images"}
	romDir, err := catalog.ROMDir(source, "FC")
	if err != nil || romDir != "/secondary/Roms/NES" {
		t.Fatalf("ROMDir = %q, %v", romDir, err)
	}
	imageDir, err := catalog.ImageDir(source, "PICO8")
	if err != nil || imageDir != "/secondary/Images/PICO8" {
		t.Fatalf("ImageDir = %q, %v", imageDir, err)
	}
	for code, id := range map[string]string{"GB": "GB", "GBC": "GBC", "GBA": "GBA", "NES": "FC", "MD": "MD", "P8": "PICO8", "PSX": "PS"} {
		system, ok := catalog.SystemForFeedCode(code)
		if !ok || system.ID != id {
			t.Errorf("feed %s mapped to %#v, want %s", code, system, id)
		}
	}
}

func TestEnsureAppDirsCreatesOnlyOwnedRoots(t *testing.T) {
	base := t.TempDir()
	env := Environment{UserdataPath: filepath.Join(base, "userdata"), LogsPath: filepath.Join(base, "logs")}
	if err := env.EnsureAppDirs(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{env.StateDir(), env.LogsPath} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Errorf("expected directory %s", path)
		}
	}
	if strings.Contains(env.StateDir(), ".system") {
		t.Fatalf("state dir entered release-managed tree: %s", env.StateDir())
	}
	if filepath.Base(env.StateDir()) != "Syncthing" {
		t.Fatalf("state dir = %s, want Syncthing app root", env.StateDir())
	}
}
