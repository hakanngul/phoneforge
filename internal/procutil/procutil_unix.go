//go:build !windows

// Package procutil holds the small per-OS pieces of child-process group
// handling used by the lldb and simulator supervisors.
package procutil

import (
	"os/exec"
	"syscall"
)

// SetProcessGroup puts the child in its own process group.
func SetProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// Interrupt sends SIGINT to the child's process group.
func Interrupt(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
	}
}

// Terminate sends SIGTERM to the child's process group.
func Terminate(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}

// KillGroup sends SIGKILL to the child's process group.
func KillGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// Detach starts the child in its own session so it survives the parent and its terminal.
func Detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
