//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// hideConsoleWindow 抑制子进程弹出命令行窗口。
// HideWindow 是 Windows 专有字段，其他平台的 syscall.SysProcAttr 没有该字段。
func hideConsoleWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}