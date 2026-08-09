//go:build !linux

package controller

import "os"

func syncStateFilesystem(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}
