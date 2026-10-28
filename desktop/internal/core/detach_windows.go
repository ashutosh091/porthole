//go:build windows

package core

import (
	"os/exec"
	"syscall"
)

// detach lets launched browsers outlive porthole.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000008} // DETACHED_PROCESS
}
