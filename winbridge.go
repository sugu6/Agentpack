//go:build windows

package main

// Windows 原生桥接 — 修补 Wails v3 alpha 的已知问题:
// 1. Mica 模式下窗口类背景画刷未替换为透明（导致灰色背景）
// 2. 无运行时 SetTheme API（标题栏不跟随应用主题切换）

import (
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32Proc           = syscall.NewLazyDLL("user32.dll")
	gdi32Proc            = syscall.NewLazyDLL("gdi32.dll")
	dwmapiProc           = syscall.NewLazyDLL("dwmapi.dll")
	procSetClassLongPtr  = user32Proc.NewProc("SetClassLongPtrW")
	procCreateSolidBrush = gdi32Proc.NewProc("CreateSolidBrush")
	procSetWindowPos     = user32Proc.NewProc("SetWindowPos")
	procDwmSetWindowAttr = dwmapiProc.NewProc("DwmSetWindowAttribute")
)

// EmptyWorkingSet 位于 psapi.dll，GetCurrentProcess 位于 kernel32.dll
var (
	psapiProc             = syscall.NewLazyDLL("psapi.dll")
	kernel32Proc          = syscall.NewLazyDLL("kernel32.dll")
	procEmptyWorkingSet   = psapiProc.NewProc("EmptyWorkingSet")
	procGetCurrentProcess = kernel32Proc.NewProc("GetCurrentProcess")
)

const (
	// GCLP_HBRBACKGROUND = -10 用二进制补码表示
	gclpHbrBackground         = ^uintptr(10)
	swpFrameChanged           = 0x0020
	swpNoMove                 = 0x0002
	swpNoSize                 = 0x0001
	swpNoZOrder               = 0x0004
	swpNoActivate             = 0x0010
	swpShowWindow             = 0x0040
	dwmwaUseImmersiveDarkMode = 20
)

// hwndCache 缓存已处理的窗口句柄
var hwndCache struct {
	mu  sync.Mutex
	set map[uintptr]struct{}
}

var (
	mainWindowHWND uintptr
	hwndMu         sync.RWMutex
)

func init() {
	hwndCache.set = make(map[uintptr]struct{})
}

// getMainWindowHWND 返回主窗口句柄。
func getMainWindowHWND() uintptr {
	hwndMu.RLock()
	defer hwndMu.RUnlock()
	return mainWindowHWND
}

// WndProcHook 是传给 application.Options.Windows.WndProcInterceptor 的回调。
// 在每次 Windows 消息到达时检查是否需要修补窗口。
func WndProcHook(hwnd uintptr, msg uint32, wParam, lParam uintptr) (uintptr, bool) {
	switch msg {
	case 0x0006: // WM_ACTIVATE — 窗口激活时修补背景（仅首次生效，后续命中缓存跳过）
		hwndCache.mu.Lock()
		_, done := hwndCache.set[hwnd]
		if !done {
			hwndCache.set[hwnd] = struct{}{}
		}
		hwndCache.mu.Unlock()
		if !done {
			fixBackground(hwnd)
			// 缓存主窗口 HWND 供 SetTheme 使用
			hwndMu.Lock()
			if mainWindowHWND == 0 {
				mainWindowHWND = hwnd
			}
			hwndMu.Unlock()
		}
	case 0x001A: // WM_SETTINGCHANGE — 检测系统主题切换
		// lParam 指向变更的设置名称字符串
		if lParam != 0 {
			// WM_SETTINGCHANGE 的 lParam 是指向 NUL 结尾 UTF-16 字符串的指针。
			// 通过取 lParam 变量地址再解引用还原该指针，避免 uintptr→Pointer
			// 转换（go vet unsafeptr 检查）。
			setting := windows.UTF16PtrToString(*(**uint16)(unsafe.Pointer(&lParam)))
			if setting == "ImmersiveColorSet" {
				hwndMu.RLock()
				h := mainWindowHWND
				hwndMu.RUnlock()
				if h != 0 {
					dark := isDarkMode()
					SetDarkMode(h, dark)
				}
			}
		}
	}
	return 0, false
}

// fixBackground 替换窗口类背景画刷为透明，
// 解决 v3 BackgroundTypeTranslucent 下 COLOR_BTNFACE 灰色画刷残留的问题。
func fixBackground(hwnd uintptr) {
	ret, _, _ := procCreateSolidBrush.Call(0x00000000) // 透明黑画刷
	if ret == 0 {
		return
	}
	procSetClassLongPtr.Call(hwnd, gclpHbrBackground, ret)
	// 触发窗口重绘以应用新画刷
	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
		swpFrameChanged|swpNoMove|swpNoSize|swpNoZOrder|swpNoActivate|swpShowWindow)
}

// SetDarkMode 设置原生标题栏的深色/浅色模式。
// 通过 DwmSetWindowAttribute(DWMWA_USE_IMMERSIVE_DARK_MODE) 实现。
func SetDarkMode(hwnd uintptr, dark bool) {
	var val int32
	if dark {
		val = 1
	}
	procDwmSetWindowAttr.Call(hwnd, uintptr(dwmwaUseImmersiveDarkMode),
		uintptr(unsafe.Pointer(&val)), unsafe.Sizeof(val))
	// 强制刷新标题栏
	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
		swpFrameChanged|swpNoMove|swpNoSize|swpNoZOrder|swpNoActivate|swpShowWindow)
}

// isDarkMode 读取 Windows 注册表判断当前系统是否为深色模式。
// 对应 w32.IsCurrentlyDarkMode() 的应用层实现。
func isDarkMode() bool {
	var key windows.Handle
	err := windows.RegOpenKeyEx(
		windows.HKEY_CURRENT_USER,
		windows.StringToUTF16Ptr(`SOFTWARE\Microsoft\Windows\CurrentVersion\Themes\Personalize`),
		0,
		windows.KEY_QUERY_VALUE,
		&key,
	)
	if err != nil {
		return false
	}
	defer windows.RegCloseKey(key)

	var val uint32
	var bufLen uint32 = 4
	err = windows.RegQueryValueEx(
		key,
		windows.StringToUTF16Ptr("AppsUseLightTheme"),
		nil,
		nil,
		(*byte)(unsafe.Pointer(&val)),
		&bufLen,
	)
	if err != nil {
		return false
	}
	return val == 0
}

// TrimWorkingSet 将当前进程的工作集页面换出，降低任务管理器中显示的内存占用。
// EmptyWorkingSet 只是提示操作系统回收物理页，进程再次活动时会自动重新换入。
func TrimWorkingSet() {
	handle, _, _ := procGetCurrentProcess.Call()
	if handle == 0 {
		return
	}
	procEmptyWorkingSet.Call(handle)
}
