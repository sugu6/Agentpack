//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	shell32          = windows.NewLazySystemDLL("shell32.dll")
	procShellExecute = shell32.NewProc("ShellExecuteW")
)

// launchInstallerWindows 以提权方式启动 NSIS 安装器。
// machine 级安装器请求管理员权限（RequestExecutionLevel=admin），非提权进程用
// CreateProcess（exec.Command）启动会因 ERROR_ELEVATION_REQUIRED 失败；
// ShellExecuteW 的 "runas" verb 会触发 UAC 提权，从而正确启动安装器。
func launchInstallerWindows(path string) error {
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	r, _, _ := procShellExecute.Call(
		0, // hwnd
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		0, // parameters
		0, // directory
		5, // SW_SHOW
	)
	if int(r) <= 32 {
		return fmt.Errorf("ShellExecuteW failed: %d", int(r))
	}
	return nil
}

// hideConsoleWindow 抑制子进程弹出命令行窗口。
// HideWindow 是 Windows 专有字段，其他平台的 syscall.SysProcAttr 没有该字段。
func hideConsoleWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
