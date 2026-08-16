//go:build !windows

package main

import (
	"fmt"
	"os/exec"
)

// hideConsoleWindow 在非 Windows 平台为空实现。
func hideConsoleWindow(cmd *exec.Cmd) {}

// launchInstallerWindows 在非 Windows 平台不会被调用（InstallUpdate 的 windows 分支
// 仅在 Windows 上执行），此处仅为满足跨平台编译提供桩实现。
func launchInstallerWindows(path string) error {
	return fmt.Errorf("launchInstallerWindows is only supported on Windows")
}
