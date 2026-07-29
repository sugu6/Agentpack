//go:build windows

package skills

import (
	"os/exec"
	"syscall"
)

// hideGitConsoleWindow 抑制 git 子进程弹出命令行窗口。
// HideWindow 是 Windows 专有字段，其他平台的 syscall.SysProcAttr 没有该字段。
func hideGitConsoleWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
