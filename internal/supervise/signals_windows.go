//go:build windows

package supervise

import (
	"os/exec"
	"syscall"
)

// Windows has no SIGUSR1 or SIGUSR2, so the two controls that wait for a
// loop to go idle have no equivalent here. The values below exist to name
// the two requests; signal refuses them before either is sent, so a child
// is never handed a signal it would read as something else.
const (
	sigStopIdle    = syscall.Signal(0)
	sigRestartIdle = syscall.Signal(0)
)

// idleSignals reports that the two signals above exist here.
const idleSignals = false

// setChildGroup does nothing: Windows has no per-child process group, so a
// child shares the supervisor's console and Ctrl-C reaches both.
func setChildGroup(cmd *exec.Cmd) {}
