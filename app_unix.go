//go:build !windows

package main

import "os/exec"

// hideConsoleWindow 在非 Windows 平台为空实现。
func hideConsoleWindow(cmd *exec.Cmd) {}