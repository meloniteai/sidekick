//go:build e2e && !windows

package e2e

import "syscall"

func procAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
