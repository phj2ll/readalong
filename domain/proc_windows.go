//go:build windows

package domain

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

// NewCmd 统一创建子进程。
// Wails 的 Windows 产物是 GUI 子系统，直接 spawn 控制台程序会分配一个临时
// 控制台，它被销毁时子进程会收到 CTRL_CLOSE 并以 0xC000013A 退出；
// 用 CREATE_NO_WINDOW + SW_HIDE 既修掉这个退出码，也避免闪黑窗。
func NewCmd(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	return cmd
}
