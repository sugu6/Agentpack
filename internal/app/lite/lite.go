// Package lite 实现轻量模式的状态机：空闲计时器与激活状态管理。
// 与 App/系统能力解耦——进入轻量模式时的副作用（隐藏窗口、内存回收等）
// 由调用方通过回调注入，使本包可独立单元测试。
package lite

import (
	"sync"
	"time"
)

// Mode 管理轻量模式的空闲计时器与激活状态。
type Mode struct {
	mu     sync.Mutex
	active bool
	timer  *time.Timer
	unit   time.Duration
}

// New 创建轻量模式状态机。unit 为计时单位（生产为 time.Minute，
// 测试可传 time.Millisecond 加速）；为 0 时回退到 time.Minute。
func New(unit time.Duration) *Mode {
	if unit == 0 {
		unit = time.Minute
	}
	return &Mode{unit: unit}
}

// Unit 返回计时单位（供调用方按配置计算延迟：minutes * Unit()）。
func (m *Mode) Unit() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.unit
}

// IsActive 返回是否处于轻量模式。
func (m *Mode) IsActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active
}

// SetActive 直接设置激活状态并停用空闲计时器。
// 重复设置相同状态为空操作；退出轻量模式后重新计时由 Restart 拉起。
func (m *Mode) SetActive(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == on {
		return
	}
	m.active = on
	m.stopLocked()
}

// Restart 按 delay 重建空闲计时器。delay<=0 或 onEnter 为空时仅停止不重建。
// 计时器到点且未被 SetActive(false)/Stop 取消时置 active=true 并回调 onEnter。
func (m *Mode) Restart(delay time.Duration, onEnter func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
	if delay <= 0 || onEnter == nil {
		return
	}
	var timer *time.Timer
	timer = time.AfterFunc(delay, func() {
		m.mu.Lock()
		// 竞态防护：timer 到点后回调 goroutine 可能因等待 m.mu 延迟执行。
		// 判定与状态置位在同一临界区内完成——若期间用户已退出轻量模式
		//（SetActive(false) 置 active=false 并清空 timer），此处直接丢弃。
		if m.timer != timer {
			m.mu.Unlock()
			return
		}
		m.timer = nil
		m.active = true
		m.mu.Unlock()
		if onEnter != nil {
			onEnter()
		}
	})
	m.timer = timer
}

// Stop 停止并清空计时器（不改变激活状态）。
func (m *Mode) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
}

// stopLocked 停止计时器，调用方必须已持有 m.mu。
func (m *Mode) stopLocked() {
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
}
