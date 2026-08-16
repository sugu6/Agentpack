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

// launchInstaller 在 Windows 上用 ShellExecuteW 启动安装程序。
// 安装器为 machine 级（RequestExecutionLevel=admin），关键点：
//   - 用 "runas" 动词显式触发提权：应用已是管理员则直接运行（无弹窗）；
//     应用为普通用户则弹出 UAC，且因传了有效父窗口 HWND，UAC consent 框会
//     正确锚定在应用窗口之上，不会像 hwnd=0 那样挂到后台/隐藏桌面，造成
//     "安装程序进程在后台跑、没有窗口也没有 UAC" 的假象。
//   - nShowCmd=SW_SHOWNORMAL 保证安装器首窗口以正常方式显示。
//   - 不走 cmd.exe，避免文件名中的 & 等元字符导致命令注入；非 Windows 平台
//     由 install_update_unix.go 的 launchInstaller 处理（open/xdg-open）。
func launchInstaller(exePath string) error {
	verb, err := syscall.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	file, err := syscall.UTF16PtrFromString(exePath)
	if err != nil {
		return err
	}
	// 以主窗口为父窗口运行，锚定 UAC 提权框与安装器窗口，避免后台悬挂。
	ret, _, _ := procShellExecuteW.Call(
		getMainWindowHWND(), // hwnd: 主窗口句柄（可为 0，但传有效值更稳）
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		0, // lpParameters
		0, // lpDirectory
		1, // SW_SHOWNORMAL
	)
	switch {
	case uintptr(ret) == 5:
		return fmt.Errorf("被拒绝启动安装程序（可能是访问被拒）")
	case uintptr(ret) == 1223:
		return fmt.Errorf("已取消提权，安装程序未启动")
	case uintptr(ret) > 32:
		return nil
	default:
		return fmt.Errorf("ShellExecuteW 启动安装程序失败，状态码 %d", uintptr(ret))
	}
}
