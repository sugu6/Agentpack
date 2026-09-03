// Package backup 提供备份/恢复/导入/导出流程中的纯计算逻辑：
// 从快照提取应用设置、归一化备份保留配置。不依赖 App 状态，可独立测试。
// 编排（锁/事件/持久化）由根目录 app.go 负责。
package backup

import (
	"encoding/json"
	"fmt"

	"agentpack/internal/backup"
	"agentpack/internal/config"
)

// ExtractSettingsFromSnapshot 从快照 settings.appSettings 解码应用设置，
// 供导入/恢复在应用 MCP 之前预检（settings 数据非法时提前失败，避免
// MCP 已落盘后的部分生效状态）。
func ExtractSettingsFromSnapshot(snap backup.Snapshot) (*config.Settings, error) {
	if snap.Settings == nil {
		return nil, nil
	}
	raw, ok := snap.Settings["appSettings"]
	if !ok {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, nil
	}
	data, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("restore: encode settings: %w", err)
	}
	var settings config.Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("restore: decode settings: %w", err)
	}
	settings = NormalizeConfig(settings)
	return &settings, nil
}

// NormalizeConfig 归一化备份保留配置：BackupRetention 与 BackupCount 互为兜底。
// BackupRetention 为 0 表示无限保留（不清理旧快照），只有负数才回退兜底。
func NormalizeConfig(s config.Settings) config.Settings {
	if s.BackupRetention < 0 {
		if s.BackupCount > 0 {
			s.BackupRetention = s.BackupCount
		} else {
			s.BackupRetention = config.DefaultSettings().BackupRetention
		}
	}
	if s.BackupCount <= 0 {
		s.BackupCount = s.BackupRetention
	}
	return s
}
