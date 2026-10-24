//go:build windows

package supervise

import (
	"os/exec"
	"syscall"
)

// Windows has no SIGUSR1, SIGUSR2 or SIGHUP for a child, so the controls
// that wait for a loop to go idle have no equivalent here. Signal refuses
// them before any is sent.
const (
	sigStopIdle    = syscall.Signal(0)
	sigRestartIdle = syscall.Signal(0)
	sigReload      = syscall.Signal(0)
)

const idleSignals = false

// setChildGroup does nothing: Windows has no per-child process group.
func setChildGroup(cmd *exec.Cmd) {}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
