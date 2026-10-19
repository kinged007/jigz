//go:build !windows

package supervise

import (
	"os/exec"
	"syscall"
)

// Control signals for a child sortie-loop. The child owns its loops and
// is the only thing watching them, so a stop that must wait for work to
// finish is requested by signalling it rather than by deciding here.
//
// These mirror the child's own names. Changing one means changing both.
const (
	// sigStopIdle stops each loop as it goes idle, then the run exits.
	sigStopIdle = syscall.SIGUSR1
	// sigRestartIdle replaces each loop as it goes idle and stays up.
	sigRestartIdle = syscall.SIGUSR2
)

// idleSignals reports that the two signals above exist here, which is
// what makes stop-when-idle and recycle work.
const idleSignals = true

// setChildGroup puts cmd in a new process group led by cmd itself, so
// a group-directed signal from the child or its descendants cannot
// reach the supervisor.
//
// Without it a child inherits the supervisor's group, and sortie-loop
// and the engine both tear down by signalling a process group. Stopping
// one repository then signalled a group containing every sibling and the
// supervisor itself: the whole fleet died instead of one loop. Each
// child is a group leader instead, so a group signal stays inside the
// subtree that owns it.
func setChildGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}
