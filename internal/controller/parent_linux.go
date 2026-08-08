//go:build linux

package controller

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// ArmParentDeath closes the set-then-recheck race for the controller itself.
// SIGTERM is catchable so the later guardian path can terminate and verify the
// complete upstream process group before releasing the generation lease.
func ArmParentDeath() error {
	parent := os.Getppid()
	if parent <= 1 {
		return errors.New("leaf-syncthing: no live supervisor parent")
	}
	if err := unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(unix.SIGTERM), 0, 0, 0); err != nil {
		return fmt.Errorf("leaf-syncthing: arm parent-death signal: %w", err)
	}
	if os.Getppid() != parent {
		_ = unix.Kill(os.Getpid(), unix.SIGTERM)
		return errors.New("leaf-syncthing: supervisor exited while arming parent-death signal")
	}
	return nil
}
