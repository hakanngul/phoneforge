//go:build windows

// Package procutil holds the small per-OS pieces of child-process group
// handling used by the lldb and simulator supervisors.
package procutil

import (
	"os/exec"
	"strconv"
	"syscall"
)

const createNewProcessGroup = 0x00000200

// SetProcessGroup starts the child in a new process group.
func SetProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// Interrupt is a no-op on Windows (no SIGINT for other processes).
func Interrupt(cmd *exec.Cmd) {}

// Terminate kills the child tree.
func Terminate(cmd *exec.Cmd) { KillGroup(cmd) }

// KillGroup kills the child and its descendants.
func KillGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	_ = cmd.Process.Kill()
}

// Detach starts the child detached from the parent's console.
func Detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | 0x00000008} // DETACHED_PROCESS
}
