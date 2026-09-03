package lite

import (
	"testing"
	"time"
)

func TestMode_New_NotActive(t *testing.T) {
	m := New(0)
	if m.IsActive() {
		t.Fatal("expected lite mode off initially")
	}
	if m.Unit() != time.Minute {
		t.Errorf("expected default unit time.Minute, got %v", m.Unit())
	}
}

func TestMode_SetActive_Idempotent(t *testing.T) {
	m := New(time.Millisecond)
	m.SetActive(true)
	if !m.IsActive() {
		t.Error("expected active after SetActive(true)")
	}
	m.SetActive(true)
	m.SetActive(false)
	if m.IsActive() {
		t.Error("expected inactive after SetActive(false)")
	}
}

func TestMode_Restart_DisabledDoesNotSchedule(t *testing.T) {
	m := New(time.Millisecond)
	m.Restart(0, func() {})
	m.Restart(time.Millisecond, nil)
	// 无计时器可观察：立即进入不应发生
	m.mu.Lock()
	timer := m.timer
	m.mu.Unlock()
	if timer != nil {
		t.Error("expected no timer when delay<=0 or onEnter==nil")
	}
}

func TestMode_Restart_EnabledSchedulesAndFires(t *testing.T) {
	m := New(time.Millisecond)
	fired := make(chan bool, 1)
	m.Restart(10*time.Millisecond, func() { fired <- true })

	m.mu.Lock()
	timer := m.timer
	m.mu.Unlock()
	if timer == nil {
		t.Fatal("expected timer to be scheduled")
	}

	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("timer did not fire within 1s")
	}
	if !m.IsActive() {
		t.Error("expected lite mode active after timer fired")
	}
}

func TestMode_SetActiveStopsTimer(t *testing.T) {
	m := New(time.Millisecond)
	fired := false
	m.Restart(10*time.Millisecond, func() { fired = true })
	m.SetActive(true) // 进入轻量模式会停用计时器
	time.Sleep(30 * time.Millisecond)
	if !m.IsActive() {
		t.Error("expected active after SetActive(true)")
	}
	if fired {
		t.Error("expected timer stopped when entering lite mode, should not fire")
	}
	// 退出后等待超过原定时长也不应重新进入
	m.SetActive(false)
	time.Sleep(30 * time.Millisecond)
	if m.IsActive() {
		t.Error("expected lite mode stay off after SetActive(false)")
	}
}

func TestMode_StopStopsTimer(t *testing.T) {
	m := New(time.Millisecond)
	m.Restart(10*time.Millisecond, func() {})
	m.Stop()
	time.Sleep(30 * time.Millisecond)
	if m.IsActive() {
		t.Error("expected lite mode to stay off after Stop")
	}
	m.mu.Lock()
	timer := m.timer
	m.mu.Unlock()
	if timer != nil {
		t.Error("expected timer cleared after Stop")
	}
}
