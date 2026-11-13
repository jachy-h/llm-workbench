//go:build !windows

package daemon

import (
	"os/exec"
	"syscall"
)

// detach puts the child in its own session so closing the terminal (SIGHUP)
// does not take the proxy down with it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
