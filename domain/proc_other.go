//go:build !windows

package domain

import "os/exec"

// NewCmd 在非 Windows 平台上就是普通命令。
func NewCmd(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}
