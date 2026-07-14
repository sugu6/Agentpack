package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// setTempHome 将 HOME 和 USERPROFILE 指向临时目录，隔离测试对真实 home 的依赖
func setTempHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	return tmp
}

func TestAgentPackDir(t *testing.T) {
	dir := AgentPackDir()
	if dir == "" {
		t.Fatal("AgentPackDir returned empty")
	}
}

func TestConfigRoundTrip(t *testing.T) {
	setTempHome(t)

	cfg := Default()
	cfg.Settings.Theme = "dark"
	cfg.Settings.AutoBackup = false
	cfg.Settings.BackupCount = 25
	cfg.DisabledAgents = []string{"claude-code", "opencode"}

	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}

	loaded := Load()
	if loaded.Settings.Theme != "dark" {
		t.Errorf("expected dark, got %s", loaded.Settings.Theme)
	}
	if loaded.Settings.AutoBackup {
		t.Errorf("expected autoBackup false, got true")
	}
	if loaded.Settings.BackupCount != 25 {
		t.Errorf("expected 25, got %d", loaded.Settings.BackupCount)
	}
	if len(loaded.DisabledAgents) != 2 {
		t.Errorf("expected 2 disabled, got %d", len(loaded.DisabledAgents))
	}
}

func TestConfigLoad_Missing(t *testing.T) {
	setTempHome(t)

	cfg := Load()
	if cfg.Settings.Theme == "" {
		t.Error("expected default theme, got empty")
	}
	if !cfg.Settings.AutoBackup {
		t.Error("expected default autoBackup true")
	}
}

func TestConfigLoad_Corrupt(t *testing.T) {
	setTempHome(t)

	dir := AgentPackDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := Load()
	if cfg.Settings.Theme == "" {
		t.Error("expected default theme after corrupt load, got empty")
	}
	if LastLoadError() == nil {
		t.Fatal("expected load error for corrupt config")
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("expected corrupt config to be quarantined, stat err=%v", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "config.json.corrupt-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one quarantined config, got %d", len(matches))
	}
}

func TestConfigLoad_CorruptWithExistingQuarantine(t *testing.T) {
	setTempHome(t)

	dir := AgentPackDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	// 预创建隔离文件，模拟旧的损坏文件
	oldQuarantine := filepath.Join(dir, "config.json.corrupt-1700000000000000000")
	if err := os.WriteFile(oldQuarantine, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	// 写入新的损坏配置
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := Load()
	if cfg.Settings.Theme == "" {
		t.Error("expected default theme after corrupt load, got empty")
	}
	if LastLoadError() == nil {
		t.Fatal("expected load error for corrupt config")
	}

	// 应有两个隔离文件（旧 + 新）
	matches, err := filepath.Glob(filepath.Join(dir, "config.json.corrupt-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) < 2 {
		t.Fatalf("expected at least 2 quarantined files, got %d", len(matches))
	}
}

// TestConfigLoad_MigratesMissingMarketSources 验证旧 config 文件缺少新增的
// marketSource key（如 smithery）时，Load 会自动补全为默认值，避免被误判为禁用
func TestConfigLoad_MigratesMissingMarketSources(t *testing.T) {
	setTempHome(t)

	dir := AgentPackDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	// 模拟旧 config：只有 official，缺少 github / skills-sh / smithery
	oldCfg := map[string]any{
		"version": 1,
		"settings": map[string]any{
			"theme": "dark",
			"marketSources": map[string]any{
				"official": map[string]any{"enabled": true},
			},
		},
		"disabledAgents": []string{},
	}
	data, err := json.Marshal(oldCfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	cfg := Load()

	// 原有的 official 应保留
	if !cfg.Settings.MarketSources["official"].Enabled {
		t.Error("expected official to remain enabled")
	}
	// 缺失的 smithery / github / skills-sh 应被补全为默认值
	for _, key := range []string{"smithery", "github", "skills-sh"} {
		ms, exists := cfg.Settings.MarketSources[key]
		if !exists {
			t.Errorf("expected market source %q to be backfilled, but it's missing", key)
			continue
		}
		if !ms.Enabled {
			t.Errorf("expected backfilled market source %q to be enabled (default), got disabled", key)
		}
	}
}
