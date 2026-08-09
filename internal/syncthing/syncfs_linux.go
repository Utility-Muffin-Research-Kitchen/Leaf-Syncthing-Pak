//go:build linux

package syncthing

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func syncFilesystemAt(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := unix.Syncfs(int(directory.Fd())); err != nil {
		return fmt.Errorf("syncfs: %w", err)
	}
	return nil
}
