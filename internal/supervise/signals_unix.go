//go:build !windows

package supervise

import (
	"os/exec"
	"syscall"
)

// Control signals for a loop child. The child owns its engine loops and
// is the only thing watching them, so a stop that must wait for work to
// finish is requested by signalling it rather than by deciding here.
//
// These mirror the child's own names. Changing one means changing both.
const (
	// sigStopIdle stops each loop as it goes idle, then the run exits.
	sigStopIdle = syscall.SIGUSR1
	// sigRestartIdle replaces each loop as it goes idle and stays up.
	sigRestartIdle = syscall.SIGUSR2
	// sigReload re-reads config.yaml without restarting the loops.
	sigReload = syscall.SIGHUP
)

// idleSignals reports that the signals above exist here.
const idleSignals = true

// setChildGroup puts cmd in a new process group led by cmd itself, so
// a group-directed signal from the child or its descendants cannot
// reach the supervisor, and a group kill reaches the engine the child
// started.
func setChildGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killGroup kills the process group cmd leads.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil && cmd.Process.Pid > 0 {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
