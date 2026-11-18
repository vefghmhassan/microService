//go:build unix

package main

import (
	"os"
	"syscall"
)

// reexec replaces the process with a fresh copy of itself (same PID, so it
// works under docker and under a plain `go run` alike).
func reexec() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}
