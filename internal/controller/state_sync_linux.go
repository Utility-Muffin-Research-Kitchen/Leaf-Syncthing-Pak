//go:build linux

package controller

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func syncStateFilesystem(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := unix.Syncfs(int(file.Fd())); err != nil {
		return fmt.Errorf("sync durable controller state: %w", err)
	}
	return nil
}
