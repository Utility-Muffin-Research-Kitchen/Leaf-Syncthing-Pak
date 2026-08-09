//go:build !linux

package syncthing

import "os/exec"

func configureChild(*exec.Cmd) {}
