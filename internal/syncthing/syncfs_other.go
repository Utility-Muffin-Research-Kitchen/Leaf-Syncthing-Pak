//go:build !linux

package syncthing

import "os"

// Native development hosts may not expose syncfs(2). MLP1 always uses the
// Linux implementation; this fallback keeps native state-table tests useful.
func syncFilesystemAt(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
