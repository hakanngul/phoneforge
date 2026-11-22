//go:build !windows

package lifecycle

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || err == syscall.EPERM
}

// parentInfo returns the parent pid and the parent's command name.
func parentInfo() (int, string) {
	ppid := os.Getppid()
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(ppid)).Output()
	if err != nil {
		return ppid, ""
	}
	return ppid, strings.TrimSpace(string(out))
}
