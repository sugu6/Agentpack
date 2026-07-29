package main

import (
	"testing"
	"time"

	"agentpack/internal/config"
)

func newLiteTestApp(enabled bool, delay int) *App {
	cfg := config.Default()
	cfg.Settings.LiteAutoEnabled = enabled
	cfg.Settings.LiteAutoDelay = delay
	a := NewApp(cfg)
	a.closed = true // 阻断真实的窗口调用，wailsApp 为 nil
	return a
}

func TestSetLiteMode_Idempotent(t *testing.T) {
	a := newLiteTestApp(false, 5)

	if a.IsLiteMode() {
		t.Fatal("expected lite mode off initially")
	}

	var changes []bool
	onLiteModeChanged = func(on bool) { changes = append(changes, on) }
	defer func() { onLiteModeChanged = nil }()

	a.SetLiteMode(true)
	if !a.IsLiteMode() {
		t.Error("expected lite mode on after SetLiteMode(true)")
	}
	a.SetLiteMode(true)
	a.SetLiteMode(false)
	if a.IsLiteMode() {
		t.Error("expected lite mode off after SetLiteMode(false)")
	}

	if len(changes) != 2 {
		t.Fatalf("expected 2 callback invocations (no duplicates), got %d: %v", len(changes), changes)
	}
	if changes[0] != true || changes[1] != false {
		t.Errorf("expected callbacks [true false], got %v", changes)
	}
}

func TestRestartLiteTimer_DisabledDoesNotSchedule(t *testing.T) {
	a := newLiteTestApp(false, 5)
	a.restartLiteTimer()

	a.liteMu.Lock()
	timer := a.liteTimer
	a.liteMu.Unlock()

	if timer != nil {
		t.Error("expected no timer when liteAutoEnabled is false")
	}
}

func TestRestartLiteTimer_EnabledSchedules(t *testing.T) {
	a := newLiteTestApp(true, 5)
	a.restartLiteTimer()

	a.liteMu.Lock()
	timer := a.liteTimer
	a.liteMu.Unlock()

	if timer == nil {
		t.Fatal("expected timer to be scheduled when liteAutoEnabled is true")
	}
	a.stopLiteTimer()

	a.liteMu.Lock()
	timer = a.liteTimer
	a.liteMu.Unlock()
	if timer != nil {
		t.Error("expected timer to be cleared after stopLiteTimer")
	}
}

func TestLiteTimer_FiresAndEntersLiteMode(t *testing.T) {
	a := newLiteTestApp(true, 5)
	// 覆盖计时单位为毫秒，避免测试等待 5 分钟
	a.liteUnit = time.Millisecond
	a.restartLiteTimer()

	deadline := time.After(2 * time.Second)
	for {
		if a.IsLiteMode() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("timer did not enter lite mode within 2s")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestNotifyActivity_ExitsNothingWhenAlreadyActive(t *testing.T) {
	a := newLiteTestApp(true, 5)
	a.liteUnit = time.Millisecond
	a.NotifyActivity()

	a.liteMu.Lock()
	timer := a.liteTimer
	a.liteMu.Unlock()
	if timer == nil {
		t.Error("expected NotifyActivity to schedule a timer")
	}
	a.stopLiteTimer()
}

func TestShowWindow_ClearsLiteMode(t *testing.T) {
	a := newLiteTestApp(true, 5)
	a.liteUnit = time.Millisecond

	a.liteMu.Lock()
	a.liteMode = true
	a.liteMu.Unlock()

	a.ShowWindow()

	if a.IsLiteMode() {
		t.Error("expected ShowWindow to clear lite mode")
	}
}

// TestShowWindow_StopsTimer 验证点击「显示主界面」会停用空闲计时器，
// 而不是重新开始计时
func TestShowWindow_StopsTimer(t *testing.T) {
	a := newLiteTestApp(true, 5)
	a.liteUnit = time.Millisecond
	a.restartLiteTimer()

	a.liteMu.Lock()
	scheduled := a.liteTimer != nil
	a.liteMu.Unlock()
	if !scheduled {
		t.Fatal("precondition failed: expected a scheduled timer")
	}

	a.ShowWindow()

	a.liteMu.Lock()
	timer := a.liteTimer
	a.liteMu.Unlock()
	if timer != nil {
		t.Error("expected ShowWindow to stop the idle timer")
	}

	// 计时器已停用，等待超过原定时长也不应进入轻量模式
	time.Sleep(50 * time.Millisecond)
	if a.IsLiteMode() {
		t.Error("expected lite mode to stay off after ShowWindow stopped the timer")
	}
}

// TestSetLiteMode_OffStopsTimer 验证托盘取消勾选退出轻量模式后计时器保持停用
func TestSetLiteMode_OffStopsTimer(t *testing.T) {
	a := newLiteTestApp(true, 5)
	a.liteUnit = time.Millisecond

	a.SetLiteMode(true)
	a.SetLiteMode(false)

	a.liteMu.Lock()
	timer := a.liteTimer
	a.liteMu.Unlock()
	if timer != nil {
		t.Error("expected no timer after exiting lite mode")
	}

	time.Sleep(50 * time.Millisecond)
	if a.IsLiteMode() {
		t.Error("expected lite mode to stay off without user activity")
	}
}

// TestNotifyActivity_RestartsTimerAfterShowWindow 验证停用后由用户活动重新拉起计时
func TestNotifyActivity_RestartsTimerAfterShowWindow(t *testing.T) {
	a := newLiteTestApp(true, 5)
	a.liteUnit = time.Millisecond

	a.ShowWindow()
	a.NotifyActivity()

	deadline := time.After(2 * time.Second)
	for {
		if a.IsLiteMode() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("expected NotifyActivity to restart the timer and enter lite mode")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
