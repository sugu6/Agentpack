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
		debug.FreeOSMemory()
		TrimWorkingSet()
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
	a.liteTimer = time.AfterFunc(delay, func() {
		a.SetLiteMode(true)
	})
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
