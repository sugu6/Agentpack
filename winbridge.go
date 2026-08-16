//go:build windows

package main

// Windows 原生桥接 — 修补 Wails v3 已知问题:
// 1. Mica 模式下窗口类背景画刷未替换为透明（导致灰色背景）
// 2. 无公开运行时 SetTheme API（标题栏暗色由 SystemThemeChanged 事件驱动 SetDarkMode）

import (
	"sync"
	"syscall"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"golang.org/x/sys/windows"
)

var (
	user32Proc           = syscall.NewLazyDLL("user32.dll")
	gdi32Proc            = syscall.NewLazyDLL("gdi32.dll")
	dwmapiProc           = syscall.NewLazyDLL("dwmapi.dll")
	procSetClassLongPtr  = user32Proc.NewProc("SetClassLongPtrW")
	procGetClassLongPtr  = user32Proc.NewProc("GetClassLongPtrW")
	procGetStockObject   = gdi32Proc.NewProc("GetStockObject")
	procDeleteObject     = gdi32Proc.NewProc("DeleteObject")
	procSetWindowPos     = user32Proc.NewProc("SetWindowPos")
	procDwmSetWindowAttr = dwmapiProc.NewProc("DwmSetWindowAttribute")
	procDefWindowProc    = user32Proc.NewProc("DefWindowProcW")
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
	// WM_NCACTIVATE：窗口标题栏/非客户区激活状态变化。DWM 据其决定系统背景材质
	// （Mica/Acrylic）使用"活跃"还是"不活跃"色调——失焦时回退为去饱和中性色
	//（即用户观察到的"长时间不操作后主题泛白"）。
	wmNcActivate              = 0x0086
	// WM_NCACTIVATE 中传给 DefWindowProc 的 lParam=-1 表示抑制非客户区重绘
	ncActivateSuppressRepaint = ^uintptr(0)
	hollowBrush               = 5 // GetStockObject: HOLLOW_BRUSH（NULL_BRUSH 别名）
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
	case 0x0002: // WM_DESTROY — 窗口销毁时清理缓存句柄：
		// 若不清理，主题切换事件仍会对已销毁窗口调用 DwmSetWindowAttribute，
		// 且 wails 若重建窗口，旧句柄会阻止新窗口进入修补缓存。
		hwndCache.mu.Lock()
		delete(hwndCache.set, hwnd)
		hwndCache.mu.Unlock()
		hwndMu.Lock()
		if mainWindowHWND == hwnd {
			mainWindowHWND = 0
		}
		hwndMu.Unlock()
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
		// 系统主题切换（WM_SETTINGCHANGE/ImmersiveColorSet）由 wails 内置的
		// events.Windows.SystemThemeChanged 事件处理，见 registerSystemThemeHook。
	case wmNcActivate:
		// Windows 11 Mica/Acrylic 在窗口失焦时由 DWM 自动降级为去饱和中性色
		//（表现为主题泛白），且无系统级开关可关闭。这里在窗口失焦（wParam==FALSE）
		// 时向 DefWindowProc 转发 wParam=TRUE（lParam=-1 抑制重绘），欺骗 DWM
		// 使其持续保留"活跃"的 Mica 色调。真实焦点（WM_SETFOCUS/KILLFOCUS）不受影响，
		// 仅拦截失焦这一种情况。此做法与 wezterm / Windows Terminal 保持背景材质的
		// 成熟方案一致。
		if wParam == 0 {
			res, _, _ := procDefWindowProc.Call(hwnd, uintptr(msg), 1, ncActivateSuppressRepaint)
			return res, true
		}
	}
	return 0, false
}

// registerSystemThemeHook 在系统深色/浅色切换时同步原生标题栏。
// v3 beta 内置了 WM_SETTINGCHANGE → SystemThemeChanged 应用事件，
// 取代旧版在 WndProcHook 中手动解析 "ImmersiveColorSet" 的实现。
//
// themeGetter 返回当前主题配置（"dark"/"light"/"system"）。仅在 system
// 主题下跟随系统切换；固定主题下若跟随，系统切换会把标题栏拉回系统色，
// 与前端固定不变的 UI 产生视觉撕裂，直到用户在设置里重切一次主题才恢复。
func registerSystemThemeHook(app *application.App, themeGetter func() string) {
	app.Event.OnApplicationEvent(events.Windows.SystemThemeChanged, func(e *application.ApplicationEvent) {
		hwnd := getMainWindowHWND()
		if hwnd == 0 {
			return
		}
		switch themeGetter() {
		case "light":
			SetDarkMode(hwnd, false)
		case "dark":
			SetDarkMode(hwnd, true)
		default:
			// system（或未知/空）：跟随系统
			SetDarkMode(hwnd, e.Context().IsDarkMode())
		}
	})
}

// fixBackground 替换窗口类背景画刷为空画刷（HOLLOW_BRUSH，不填充背景），
// 解决 v3 BackgroundTypeTranslucent 下 COLOR_BTNFACE 灰色画刷残留的问题。
// 注意：不能用 CreateSolidBrush(0)——那创建的是不透明纯黑实体画刷，并非
// 透明；若 wails 走系统擦除路径（WM_ERASEBKGND），窗口背景会被刷成纯黑。
func fixBackground(hwnd uintptr) {
	ret, _, _ := procGetStockObject.Call(hollowBrush)
	if ret == 0 {
		return
	}
	oldBrush, _, _ := procSetClassLongPtr.Call(hwnd, gclpHbrBackground, ret)
	// 回读窗口类当前采用的画刷，确认替换是否生效，
	// 避免在替换失败（返回 0）时误删仍由类持有的句柄。
	current, _, _ := procGetClassLongPtr.Call(hwnd, gclpHbrBackground)
	if current != ret {
		// 替换失败：stock 对象由系统管理，无需也不应释放
		return
	}
	// 替换成功：释放被替换的旧画刷，避免 GDI 对象泄漏。
	// 旧画刷为 NULL 或系统 stock 对象（如默认 COLOR_BTNFACE）时
	// DeleteObject 静默返回 0，调用安全。
	procDeleteObject.Call(oldBrush)
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
