//go:build windows

package i18n

import (
	"syscall"
	"unsafe"
)

// detectSystemLanguageOS 调用 Windows API 获取用户默认 locale 名
// 返回值如 "zh-CN"、"en-US",失败返回 ""
func detectSystemLanguageOS() string {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GetUserDefaultLocaleName")
	buf := make([]uint16, 85) // LOCALE_NAME_MAX_LENGTH
	r, _, _ := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}
