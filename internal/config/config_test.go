package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	// 缺失的 github / skills-sh 应被补全为默认值
	for _, key := range []string{"github", "skills-sh"} {
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

// TestClampLiteDelay validates ClampLiteDelay's bounds and default fallback
func TestClampLiteDelay(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, liteDelayDefault},
		{-10, liteDelayDefault},
		{1, liteDelayMin},
		{5, liteDelayDefault},
		{120, liteDelayMax},
		{121, liteDelayMax},
		{99999, liteDelayMax},
	}
	for _, c := range cases {
		if got := ClampLiteDelay(c.in); got != c.want {
			t.Errorf("ClampLiteDelay(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestConfigLoad_LiteDefaults verifies that an old config without lite fields gets defaults
func TestConfigLoad_LiteDefaults(t *testing.T) {
	setTempHome(t)

	dir := AgentPackDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	// Only have theme, no lite fields
	oldCfg := map[string]any{
		"version": 1,
		"settings": map[string]any{
			"theme": "dark",
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
	if cfg.Settings.LiteAutoEnabled {
		t.Error("expected liteAutoEnabled to default to false")
	}
	if cfg.Settings.LiteAutoDelay != liteDelayDefault {
		t.Errorf("expected liteAutoDelay default %d, got %d", liteDelayDefault, cfg.Settings.LiteAutoDelay)
	}
}

// TestConfigLoad_BackupRetentionZeroIsPreserved 验证显式的 backupRetention=0
// （语义：无限保留）不会被 Load 覆盖为默认值；仅字段缺失（旧配置迁移）才填默认。
func TestConfigLoad_BackupRetentionZeroIsPreserved(t *testing.T) {
	setTempHome(t)

	dir := AgentPackDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	// 显式写入 backupRetention: 0（无限保留）
	explicitCfg := map[string]any{
		"version": 1,
		"settings": map[string]any{
			"backupRetention": 0,
			"backupCount":     10,
		},
		"disabledAgents": []string{},
	}
	data, err := json.Marshal(explicitCfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	cfg := Load()
	if cfg.Settings.BackupRetention != 0 {
		t.Errorf("expected backupRetention 0 to be preserved (infinite retention), got %d", cfg.Settings.BackupRetention)
	}
}

// TestConfigLoad_BackupRetentionMissingGetsDefault 验证旧配置缺少 backupRetention
// 字段时（默认 int 0 与"无限保留"语义冲突），应填默认值 50。
func TestConfigLoad_BackupRetentionMissingGetsDefault(t *testing.T) {
	setTempHome(t)

	dir := AgentPackDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	oldCfg := map[string]any{
		"version": 1,
		"settings": map[string]any{
			"theme": "dark",
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
	if cfg.Settings.BackupRetention != DefaultSettings().BackupRetention {
		t.Errorf("expected backupRetention default %d when field missing, got %d",
			DefaultSettings().BackupRetention, cfg.Settings.BackupRetention)
	}
}

// TestConfigRoundTrip_Lite ensures lite fields survive Save/Load cycle
func TestConfigRoundTrip_Lite(t *testing.T) {
	setTempHome(t)

	cfg := Default()
	cfg.Settings.LiteAutoEnabled = true
	cfg.Settings.LiteAutoDelay = 30
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}

	loaded := Load()
	if !loaded.Settings.LiteAutoEnabled {
		t.Error("expected liteAutoEnabled true after round trip")
	}
	if loaded.Settings.LiteAutoDelay != 30 {
		t.Errorf("expected liteAutoDelay 30, got %d", loaded.Settings.LiteAutoDelay)
	}
}

// TestSanitizePathError 验证路径类错误中的完整路径被替换为基名（L2 回归）。
func TestSanitizePathError(t *testing.T) {
	full := filepath.Join("C:\\Users", "alice", ".agentpack", "config.json")
	secret := filepath.Join("C:\\Users", "alice", ".agentpack", "config.json.corrupt-1")
	renameErr := &os.LinkError{Op: "rename", Old: full, New: secret, Err: os.ErrPermission}
	got := sanitizePathError(renameErr)
	if got == "" {
		t.Fatal("expected non-empty sanitized error")
	}
	if strings.Contains(got, "alice") || strings.Contains(got, "C:\\Users") {
		t.Errorf("sanitized error must not contain full paths, got %q", got)
	}
	if !strings.Contains(got, "config.json") {
		t.Errorf("sanitized error should retain base names, got %q", got)
	}
	// 非路径错误原样返回
	if sanitizePathError(os.ErrPermission) != os.ErrPermission.Error() {
		t.Error("non-path error should pass through unchanged")
	}
}

func TestConfigLoad_PreservesUnknownFields(t *testing.T) {
	setTempHome(t)

	dir := AgentPackDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	oldCfg := map[string]any{
		"version":        1,
		"disabledAgents": []string{},
		"customTopLevel": "keep-me",
		"settings": map[string]any{
			"theme":            "dark",
			"customNested":     42,
			"futureNestedFlag": true,
		},
	}
	data, err := json.Marshal(oldCfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	cfg := Load()
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["customTopLevel"] != "keep-me" {
		t.Errorf("top-level unknown field lost after Save: %v", saved["customTopLevel"])
	}
	settings, ok := saved["settings"].(map[string]any)
	if !ok {
		t.Fatalf("settings not an object: %T", saved["settings"])
	}
	if settings["customNested"] != float64(42) {
		t.Errorf("nested unknown field lost after Save: %v", settings["customNested"])
	}
	if settings["futureNestedFlag"] != true {
		t.Errorf("nested unknown bool field lost after Save: %v", settings["futureNestedFlag"])
	}
}

func TestConfigLoad_BackupCountZeroIsPreserved(t *testing.T) {
	setTempHome(t)

	dir := AgentPackDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	oldCfg := map[string]any{
		"version":        1,
		"disabledAgents": []string{},
		"settings": map[string]any{
			"theme":           "dark",
			"backupCount":     0,
			"backupRetention": 5,
		},
	}
	data, err := json.Marshal(oldCfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	cfg := Load()
	if cfg.Settings.BackupCount != 0 {
		t.Errorf("expected explicit backupCount 0 to be preserved, got %d", cfg.Settings.BackupCount)
	}
}
