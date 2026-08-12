//go:build !linux

package syncthing

import (
	"context"
	"errors"
	"syscall"
)

func DetectForeignConflict() (Conflict, error) { return Conflict{}, nil }
func currentProcessGroup() (int, error) {
	return 0, errors.New("process-group supervision is Linux-only")
}
func processGroup(int) (int, error) { return 0, errors.New("process-group supervision is Linux-only") }
func signalGroupMembers(int, int, syscall.Signal) error {
	return errors.New("process-group supervision is Linux-only")
}
func groupAbsent(int, int) bool { return false }
func describeGroup(int, int) string {
	return "group census unavailable: process-group supervision is Linux-only"
}
func waitForGroupAbsence(context.Context, int, int) error {
	return errors.New("process-group supervision is Linux-only")
}
