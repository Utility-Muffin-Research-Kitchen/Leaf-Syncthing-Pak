//go:build linux

package syncthing

import (
	"os/exec"
	"syscall"
)

func configureChild(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
