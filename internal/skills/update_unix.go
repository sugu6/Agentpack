//go:build !windows

package skills

import "os/exec"

// hideGitConsoleWindow 在非 Windows 平台为空实现。
func hideGitConsoleWindow(cmd *exec.Cmd) {}
