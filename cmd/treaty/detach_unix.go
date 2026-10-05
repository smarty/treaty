//go:build !windows

package main

import "syscall"

// detached starts a process in its own session, so it outlives the
// terminal or agent that started it.
func detached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
