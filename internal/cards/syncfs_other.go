//go:build !linux

package cards

import "os"

func syncFilesystemAt(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}
