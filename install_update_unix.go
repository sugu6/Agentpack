//go:build !windows

package main

import (
	"os/exec"
	"runtime"
)

// launchInstaller 在非 Windows 平台用系统默认方式打开安装程序。
// macOS 用 open，Linux 用 xdg-open。
func launchInstaller(path string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", path).Start()
	}
	return exec.Command("xdg-open", path).Start()
}
