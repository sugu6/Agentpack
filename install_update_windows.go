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

// launchInstaller 在 Windows 上启动安装程序。
// 安装器为 per-user 级（WAILS_INSTALL_SCOPE=user，RequestExecutionLevel=user），
// 无需任何提权/UAC。用 ShellExecuteW "open" 交给系统 shell 启动：
//   - 以主窗口为父窗口，安装器窗口会正确浮出（避免因无父窗口挂到后台）；
//   - nShowCmd=SW_SHOWNORMAL 保证首窗口正常显示；
//   - 子进程完全脱离本应用生命周期，应用退出后安装器照常运行；
//   - 不走 cmd.exe，避免文件名中的 & 等元字符导致命令注入。
func launchInstaller(exePath string) error {
	verb, err := syscall.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := syscall.UTF16PtrFromString(exePath)
	if err != nil {
		return err
	}
	ret, _, _ := procShellExecuteW.Call(
		getMainWindowHWND(), // hwnd: 主窗口句柄，锚定安装器窗口
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		0, // lpParameters
		0, // lpDirectory
		1, // SW_SHOWNORMAL
	)
	switch {
	case uintptr(ret) == 5:
		return fmt.Errorf("被拒绝启动安装程序（可能是访问被拒）")
	case uintptr(ret) > 32:
		return nil
	default:
		return fmt.Errorf("ShellExecuteW 启动安装程序失败，状态码 %d", uintptr(ret))
	}
}
