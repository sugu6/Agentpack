package main

import (
	"runtime/debug"
	"time"

	"agentpack/internal/config"
)

// onLiteModeChanged 由 tray.go 赋值，用于在状态变化后同步托盘复选框
var onLiteModeChanged func(bool)

// IsLiteMode 返回当前是否处于轻量模式
func (a *App) IsLiteMode() bool {
	a.liteMu.Lock()
	defer a.liteMu.Unlock()
	return a.liteMode
}

// SetLiteMode 进入或退出轻量模式。重复设置相同状态为空操作。
// 进入时隐藏窗口并主动归还内存；退出时恢复窗口显示。
// 两个方向都会停用空闲计时器——重新计时只由前端上报的用户活动触发。
func (a *App) SetLiteMode(on bool) {
	a.liteMu.Lock()
	if a.liteMode == on {
		a.liteMu.Unlock()
		return
	}
	a.liteMode = on
	a.stopLiteTimerLocked()
	a.liteMu.Unlock()

	if on {
		a.HideWindow()
		// FreeOSMemory 会同步触发 StopTheWorld 式全局停顿，经托盘回调
		// 直接调用会造成短暂 UI 冻结；移入 goroutine 异步执行，
		// 由 Go 运行时自行调度，界面不感知停顿。
		go func() {
			debug.FreeOSMemory()
			TrimWorkingSet()
		}()
	} else {
		a.showWindowRaw()
	}

	if onLiteModeChanged != nil {
		onLiteModeChanged(on)
	}
}

// NotifyActivity 由前端在检测到用户活动时调用，重置空闲计时器。
// 处于轻量模式时不做任何事：退出轻量模式只能由用户显式触发。
func (a *App) NotifyActivity() {
	if a.IsLiteMode() {
		return
	}
	a.restartLiteTimer()
}

// liteConfig 读取轻量模式配置。独立成函数以确保 a.mu 在 liteMu 之外获取。
func (a *App) liteConfig() (enabled bool, delay time.Duration) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.cfg == nil {
		return false, 0
	}
	// liteUnit 仅在测试中于启动前赋值一次（不参与并发），无需同步保护
	unit := a.liteUnit
	if unit == 0 {
		unit = time.Minute
	}
	minutes := config.ClampLiteDelay(a.cfg.Settings.LiteAutoDelay)
	return a.cfg.Settings.LiteAutoEnabled, time.Duration(minutes) * unit
}

// restartLiteTimer 按当前配置重建空闲计时器。配置关闭或已处于轻量模式时仅停止不重建。
func (a *App) restartLiteTimer() {
	// liteConfig 需要 a.mu，必须在获取 liteMu 之前调用（锁定顺序约定）
	enabled, delay := a.liteConfig()

	a.liteMu.Lock()
	defer a.liteMu.Unlock()
	a.stopLiteTimerLocked()
	if !enabled || a.liteMode {
		return
	}
	// 先用 var 声明再赋值：`:=` 声明的变量作用域从语句结束后才开始，
	// 闭包内引用同一语句声明的变量会报 undefined
	var timer *time.Timer
	timer = time.AfterFunc(delay, func() {
		// 竞态防护：timer 到点后回调 goroutine 可能因等待 liteMu 延迟执行。
		// 判定与状态置位必须在同一临界区内完成——若先解锁再调 SetLiteMode(true)，
		// 窗口期用户点击托盘 ShowWindow（锁内把 liteMode 置 false、清空计时器）
		// 后，回调仍会执行 SetLiteMode(true) → 窗口刚显示又被隐藏。
		// 锁内完成置位后，无论谁先拿到锁，结果都收敛于"最后一次用户操作获胜"：
		//  ShowWindow 先 → 计时器已清空，回调直接丢弃；
		//  回调先 → liteMode 已置 true，ShowWindow 再置 false 并显示窗口。
		a.liteMu.Lock()
		if a.liteTimer != timer {
			a.liteMu.Unlock()
			return
		}
		a.liteTimer = nil
		a.liteMode = true
		a.liteMu.Unlock()

		a.HideWindow()
		debug.FreeOSMemory()
		TrimWorkingSet()
		if onLiteModeChanged != nil {
			onLiteModeChanged(true)
		}
	})
	a.liteTimer = timer
}

// stopLiteTimer 停止并清空计时器
func (a *App) stopLiteTimer() {
	a.liteMu.Lock()
	defer a.liteMu.Unlock()
	a.stopLiteTimerLocked()
}

// stopLiteTimerLocked 停止并清空计时器，调用方必须已持有 liteMu
func (a *App) stopLiteTimerLocked() {
	if a.liteTimer != nil {
		a.liteTimer.Stop()
		a.liteTimer = nil
	}
}
