//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	shell32           = syscall.NewLazyDLL("shell32.dll")
	procShellExecuteW = shell32.NewProc("ShellExecuteW")
)

// launchInstaller 在 Windows 上用 ShellExecuteW("open") 启动安装程序。
// 之所以不直接用 exec.Command（CreateProcess）：
//   - 安装器是 GUI 子系统程序，CreateProcess 作为父进程子进程启动，父进程（本应用）
//     退出时两者的 STARTUPINFO 关联可能让安装器首窗口被隐藏，表现为"退出后安装程序
//     在后台、没有窗口"；
//   - ShellExecute 交给系统 shell 启动，子进程完全脱离父进程生命周期，且用默认
//     SW_SHOWNORMAL 显示窗口，窗口必然正常弹出；
//   - 走 "open" 动词而不是 cmd.exe，避免文件名中的 & 等元字符导致命令注入；
//   - 本安装器为 user 级（无需 UAC 提权），"open" 以当前用户权限运行即可。
func launchInstaller(exePath string) error {
	verb, err := syscall.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := syscall.UTF16PtrFromString(exePath)
	if err != nil {
		return err
	}
	// ShellExecuteW 返回值大于 32 表示成功。
	// nShowCmd = SW_SHOWNORMAL(1)，确保安装器窗口以正常方式显示。
	ret, _, callErr := procShellExecuteW.Call(
		0, // hwnd
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		0, // lpParameters
		0, // lpDirectory
		1, // SW_SHOWNORMAL
	)
	if uintptr(ret) <= 32 {
		if callErr != syscall.Errno(0) {
			return fmt.Errorf("ShellExecuteW failed: %w", callErr)
		}
		return fmt.Errorf("ShellExecuteW failed with status %d", uintptr(ret))
	}
	return nil
}
