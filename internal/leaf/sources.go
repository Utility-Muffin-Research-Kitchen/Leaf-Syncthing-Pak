package leaf

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type Source struct {
	ID                 string
	Root               string
	Primary            bool
	UserdataPath       string
	SharedUserdataPath string
	RomsPath           string
	ImagesPath         string
	MusicPath          string
	AppsPath           string
	BIOSPath           string
	SavesPath          string
	StatesPath         string
	CheatsPath         string

	// MustBeMounted distinguishes an actual removable source from the empty
	// mount-point directories shipped by the MLP1 root filesystem.
	MustBeMounted bool
}

func (s Source) Available() bool {
	info, err := os.Stat(s.Root)
	if err != nil || !info.IsDir() {
		return false
	}
	if !s.MustBeMounted || runtime.GOOS != "linux" {
		return true
	}
	mountInfo, err := os.ReadFile("/proc/self/mountinfo")
	return err == nil && mountInfoHasRoot(mountInfo, s.Root)
}

func mountInfoHasRoot(mountInfo []byte, root string) bool {
	want := filepath.Clean(root)
	for _, line := range strings.Split(string(mountInfo), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		mountPoint := strings.NewReplacer(
			`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`,
		).Replace(fields[4])
		if filepath.Clean(mountPoint) == want {
			return true
		}
	}
	return false
}

type SourceList []Source

func (s SourceList) Primary() (Source, bool) {
	if len(s) == 0 {
		return Source{}, false
	}
	return s[0], true
}

func (s SourceList) ByID(id string) (Source, bool) {
	for _, source := range s {
		if source.ID == id {
			return source, true
		}
	}
	return Source{}, false
}

func resolveRootList(getenv getenvFunc, primary, secondary string) ([]string, error) {
	if raw, ok := getenv("SDCARD_PATHS"); ok && raw != "" {
		roots, err := parsePathList("SDCARD_PATHS", raw)
		if err != nil {
			return nil, err
		}
		if roots[0] != primary {
			return nil, fmt.Errorf("SDCARD_PATHS primary %q does not match SDCARD_PATH %q", roots[0], primary)
		}
		return roots, nil
	}
	if secondary != "" && secondary != primary {
		return []string{primary, secondary}, nil
	}
	return []string{primary}, nil
}

func parsePathList(name, raw string) ([]string, error) {
	parts := strings.Split(raw, ":")
	if len(parts) == 0 {
		return nil, fmt.Errorf("%s is empty", name)
	}
	seen := make(map[string]bool, len(parts))
	for i, part := range parts {
		if strings.TrimSpace(part) == "" {
			return nil, fmt.Errorf("%s contains an empty item at index %d", name, i)
		}
		parts[i] = cleanPath(part)
		if seen[parts[i]] {
			return nil, fmt.Errorf("%s contains duplicate root %q", name, parts[i])
		}
		seen[parts[i]] = true
	}
	return parts, nil
}

func resolveContentPaths(getenv getenvFunc, roots []string, plural, singular, child string) ([]string, error) {
	if raw, ok := getenv(plural); ok && raw != "" {
		paths, err := parsePathList(plural, raw)
		if err != nil {
			return nil, err
		}
		if len(paths) != len(roots) {
			return nil, fmt.Errorf("%s has %d items; SDCARD_PATHS has %d", plural, len(paths), len(roots))
		}
		if value, ok := getenv(singular); ok && value != "" && cleanPath(value) != paths[0] {
			return nil, fmt.Errorf("%s primary %q does not match %s %q", plural, paths[0], singular, cleanPath(value))
		}
		return paths, nil
	}
	paths := make([]string, len(roots))
	for i, root := range roots {
		paths[i] = filepath.Join(root, child)
	}
	if value, ok := getenv(singular); ok && value != "" {
		paths[0] = cleanPath(value)
	}
	return paths, nil
}

func resolveSources(getenv getenvFunc, roots []string, secondary string) (SourceList, bool, error) {
	type contentSpec struct {
		plural, singular, child string
	}
	specs := []contentSpec{
		{"ROMS_PATHS", "ROMS_PATH", "Roms"},
		{"IMAGES_PATHS", "IMAGES_PATH", "Images"},
		{"MUSIC_PATHS", "MUSIC_PATH", "Music"},
		{"APPS_PATHS", "APPS_PATH", "Apps"},
		{"BIOS_PATHS", "BIOS_PATH", "BIOS"},
		{"SAVES_PATHS", "SAVES_PATH", "Saves"},
		{"STATES_PATHS", "STATES_PATH", "States"},
		{"CHEATS_PATHS", "CHEATS_PATH", "Cheats"},
	}
	resolved := make([][]string, len(specs))
	for i, spec := range specs {
		paths, err := resolveContentPaths(getenv, roots, spec.plural, spec.singular, spec.child)
		if err != nil {
			return nil, false, err
		}
		resolved[i] = paths
	}

	userdataPaths, sharedUserdataPaths, sourcePathsV2, err := resolveSourcePathsV2(getenv, roots, resolved[5], resolved[6])
	if err != nil {
		return nil, false, err
	}

	sources := make(SourceList, len(roots))
	for i, root := range roots {
		id := fmt.Sprintf("source%d", i+1)
		if i == 0 {
			id = "primary"
		} else if root == secondary {
			id = "secondary_sd"
		}
		sources[i] = Source{
			ID: id, Root: root, Primary: i == 0,
			UserdataPath: valueAt(userdataPaths, i), SharedUserdataPath: valueAt(sharedUserdataPaths, i),
			RomsPath: resolved[0][i], ImagesPath: resolved[1][i],
			MusicPath: resolved[2][i], AppsPath: resolved[3][i],
			BIOSPath: resolved[4][i], SavesPath: resolved[5][i],
			StatesPath: resolved[6][i], CheatsPath: resolved[7][i],
			MustBeMounted: i > 0,
		}
	}
	return sources, sourcePathsV2, nil
}

func resolveSourcePathsV2(getenv getenvFunc, roots, savesPaths, statesPaths []string) ([]string, []string, bool, error) {
	version, _ := getenv("UMRK_ENV_VERSION")
	if version != "2" {
		return nil, nil, false, nil
	}

	cardRoots, err := requiredAlignedPaths(getenv, "SDCARD_PATHS", "SDCARD_PATH", len(roots))
	if err != nil {
		return nil, nil, false, err
	}
	userdataPaths, err := requiredAlignedPaths(getenv, "USERDATA_PATHS", "USERDATA_PATH", len(roots))
	if err != nil {
		return nil, nil, false, err
	}
	sharedUserdataPaths, err := requiredAlignedPaths(getenv, "SHARED_USERDATA_PATHS", "SHARED_USERDATA_PATH", len(roots))
	if err != nil {
		return nil, nil, false, err
	}
	declaredSaves, err := requiredAlignedPaths(getenv, "SAVES_PATHS", "SAVES_PATH", len(roots))
	if err != nil {
		return nil, nil, false, err
	}
	declaredStates, err := requiredAlignedPaths(getenv, "STATES_PATHS", "STATES_PATH", len(roots))
	if err != nil {
		return nil, nil, false, err
	}

	for index := range roots {
		if cardRoots[index] != roots[index] || declaredSaves[index] != savesPaths[index] || declaredStates[index] != statesPaths[index] {
			return nil, nil, false, fmt.Errorf("source-paths-v2 list order differs at index %d", index)
		}
		for name, path := range map[string]string{
			"USERDATA_PATHS": userdataPaths[index], "SHARED_USERDATA_PATHS": sharedUserdataPaths[index],
			"SAVES_PATHS": declaredSaves[index], "STATES_PATHS": declaredStates[index],
		} {
			if _, err := RelativeWithin(roots[index], path); err != nil {
				return nil, nil, false, fmt.Errorf("%s item %d is not on its declared card: %w", name, index, err)
			}
		}
	}
	return userdataPaths, sharedUserdataPaths, true, nil
}

func requiredAlignedPaths(getenv getenvFunc, plural, singular string, count int) ([]string, error) {
	rawPlural, pluralOK := getenv(plural)
	rawSingular, singularOK := getenv(singular)
	if !pluralOK || rawPlural == "" || !singularOK || rawSingular == "" {
		return nil, fmt.Errorf("source-paths-v2 requires %s and %s", plural, singular)
	}
	rawItems := strings.Split(rawPlural, ":")
	if len(rawItems) != count {
		return nil, fmt.Errorf("%s has %d items; SDCARD_PATHS has %d", plural, len(rawItems), count)
	}
	if rawItems[0] != rawSingular {
		return nil, fmt.Errorf("%s primary %q does not byte-match %s %q", plural, rawItems[0], singular, rawSingular)
	}
	paths, err := parsePathList(plural, rawPlural)
	if err != nil {
		return nil, err
	}
	return paths, nil
}

func valueAt(values []string, index int) string {
	if index < len(values) {
		return values[index]
	}
	return ""
}
