package config

import (
	"agentpack/internal/iowriter"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultGitHubProxy 是默认的 GitHub API/下载代理地址
// 用于解决中国地区无法直接访问 GitHub 的问题
var DefaultGitHubProxy = "https://gh-proxy.com/"

const (
	currentVersion = 1

	liteDelayDefault = 5
	liteDelayMin     = 1
	liteDelayMax     = 120
)

// ClampLiteDelay 将轻量模式空闲时长钳制到合法区间，非法值回落到默认值
func ClampLiteDelay(v int) int {
	if v < liteDelayMin {
		return liteDelayDefault
	}
	if v > liteDelayMax {
		return liteDelayMax
	}
	return v
}

var (
	lastLoadErrMu sync.RWMutex
	lastLoadErr   error
)

func LastLoadError() error {
	lastLoadErrMu.RLock()
	defer lastLoadErrMu.RUnlock()
	return lastLoadErr
}

func setLastLoadError(err error) {
	lastLoadErrMu.Lock()
	defer lastLoadErrMu.Unlock()
	lastLoadErr = err
}

type AppConfig struct {
	Version        int      `json:"version"`
	Settings       Settings `json:"settings"`
	DisabledAgents []string `json:"disabledAgents"`
	// extra/settingsExtra 保留未知 JSON 键（用户手写扩展字段、未来版本新增
	// 字段在旧版本运行期间）：json.Unmarshal 静默丢弃未知键，任何一次 Save
	// 全量重写都会抹掉它们——版本降级后配置悄悄丢数据。Load 时提取，
	// Save 时合并写回。
	extra         map[string]json.RawMessage `json:"-"`
	settingsExtra map[string]json.RawMessage `json:"-"`
}

type Settings struct {
	Theme           string                  `json:"theme"`
	MarketSources   map[string]MarketSource `json:"marketSources"`
	AutoBackup      bool                    `json:"autoBackup"`
	BackupCount     int                     `json:"backupCount"`
	BackupRetention int                     `json:"backupRetention"`
	SkillStorage    string                  `json:"skillStorage"`    // "agentpack" | "unified"
	SkillSyncMethod string                  `json:"skillSyncMethod"` // "symlink" | "copy"
	SkillRepos      []SkillRepo             `json:"skillRepos"`      // GitHub 仓库扫描列表（用户可配置）
	WindowAction    string                  `json:"windowAction"`    // "minimize" | "exit"
	WindowNoRemind  bool                    `json:"windowNoRemind"`  // 不再提醒，直接执行 windowAction
	Language        string                  `json:"language"`        // "" | "zh-CN" | "en"（空=跟随系统）

	LiteAutoEnabled bool `json:"liteAutoEnabled"` // 空闲后自动进入轻量模式
	LiteAutoDelay   int  `json:"liteAutoDelay"`   // 空闲时长（分钟）
}

// SkillRepo 表示一个可扫描的 GitHub 仓库
type SkillRepo struct {
	Owner  string `json:"owner"`
	Name   string `json:"name"`
	Branch string `json:"branch"` // 默认 "main"
}

type MarketSource struct {
	Enabled  bool  `json:"enabled"`
	LastSync int64 `json:"lastSync,omitempty"`
}

func DefaultSettings() Settings {
	return Settings{
		Theme: "system",
		MarketSources: map[string]MarketSource{
			"registry":  {Enabled: true},
			"github":    {Enabled: true},
			"skills-sh": {Enabled: true},
		},
		AutoBackup:      true,
		BackupCount:     10,
		BackupRetention: 50,
		SkillStorage:    "agentpack",
		SkillSyncMethod: "symlink",
		SkillRepos: []SkillRepo{
			{Owner: "anthropics", Name: "skills", Branch: ""},
			{Owner: "ComposioHQ", Name: "awesome-claude-skills", Branch: ""},
		},
		WindowAction:    "minimize",
		Language:        "",
		LiteAutoEnabled: false,
		LiteAutoDelay:   liteDelayDefault,
	}
}

func Default() *AppConfig {
	return &AppConfig{
		Version:        currentVersion,
		Settings:       DefaultSettings(),
		DisabledAgents: []string{},
	}
}

func configPath() string {
	dir := AgentPackDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "config.json")
}

// jsonHasKey 检查 data 中是否存在形如 {"a":{"b":...}} 的嵌套字段。
// 用于区分"字段缺失"与"零值字段"，两者在 Go struct 反序列化后无法区分。
func jsonHasKey(data []byte, keys ...string) bool {
	if len(keys) == 0 {
		return false
	}
	var current map[string]json.RawMessage
	if err := json.Unmarshal(data, &current); err != nil {
		return false
	}
	for i, key := range keys {
		raw, ok := current[key]
		if !ok {
			return false
		}
		if i == len(keys)-1 {
			return true
		}
		var next map[string]json.RawMessage
		if err := json.Unmarshal(raw, &next); err != nil {
			return false
		}
		current = next
	}
	return true
}

// sanitizePathError 将路径类错误（*os.LinkError / *os.PathError）中的完整路径
// 替换为基名，避免用户目录等本地路径信息通过 startupErrors 暴露到 UI。
func sanitizePathError(err error) string {
	var le *os.LinkError
	if errors.As(err, &le) {
		return fmt.Sprintf("(%s -> %s): %v", filepath.Base(le.Old), filepath.Base(le.New), le.Err)
	}
	var pe *os.PathError
	if errors.As(err, &pe) {
		return fmt.Sprintf("%s: %v", filepath.Base(pe.Path), pe.Err)
	}
	return err.Error()
}

func Load() *AppConfig {
	setLastLoadError(nil)
	path := configPath()
	if path == "" {
		return Default()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("config read: %v", err)
			setLastLoadError(fmt.Errorf("read config: %w", err))
		}
		cfg := Default()
		if saveErr := Save(cfg); saveErr != nil {
			log.Printf("config initial save: %v", saveErr)
		}
		return cfg
	}
	var cfg AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		quarantine := nextCorruptPath(path)
		if rerr := os.Rename(path, quarantine); rerr != nil {
			log.Printf("config parse failed and unable to quarantine: parse=%v rename=%v", err, rerr)
			// rerr 为 *os.LinkError，Error() 含完整路径；只展示基名，避免用户目录
			// 信息通过 startupErrors 暴露到 UI。
			setLastLoadError(fmt.Errorf("config corrupted (parse=%v, quarantine failed: %s)", err, sanitizePathError(rerr)))
		} else {
			log.Printf("config parse failed, original file moved to %s: %v", quarantine, err)
			setLastLoadError(fmt.Errorf("config corrupted, original moved to %s: %w", filepath.Base(quarantine), err))
		}
		return Default()
	}
	// 提取未知字段（见 AppConfig.extra 注释）
	cfg.extra = extractUnknownKeys(data, "version", "settings", "disabledAgents")
	cfg.settingsExtra = extractUnknownKeysOf(data, "settings",
		"theme", "marketSources", "autoBackup", "backupCount", "backupRetention",
		"skillStorage", "skillSyncMethod", "skillRepos", "windowAction",
		"windowNoRemind", "language", "liteAutoEnabled", "liteAutoDelay")
	wasOldConfig := cfg.Version == 0
	if wasOldConfig {
		cfg.Version = currentVersion
	}
	mutated := wasOldConfig
	defaults := DefaultSettings()
	if cfg.Settings.Theme == "" {
		cfg.Settings.Theme = defaults.Theme
	}
	// MarketSources: 若整体为 nil 直接用默认值；否则逐个 key 补全缺失项
	// 避免旧 config 文件缺少新增来源时被误判为禁用
	if cfg.Settings.MarketSources == nil {
		cfg.Settings.MarketSources = defaults.MarketSources
		mutated = true
	} else {
		for key, def := range defaults.MarketSources {
			if _, exists := cfg.Settings.MarketSources[key]; !exists {
				cfg.Settings.MarketSources[key] = def
				mutated = true
			}
		}
	}
	// BackupCount 语义与 backupRetention 对齐：显式 0 是用户的意图（关闭
	// 计数兜底，normalizeBackupConfig 会以 BackupRetention 兜底），
	// 只有字段缺失（旧版本迁移）才填默认值。
	if !jsonHasKey(data, "settings", "backupCount") {
		cfg.Settings.BackupCount = defaults.BackupCount
		mutated = true
	}
	// 值域校验：手写/损坏配置中的非法值回落默认并写回，避免 UI 行为不可预期
	switch cfg.Settings.Theme {
	case "system", "light", "dark":
	default:
		cfg.Settings.Theme = defaults.Theme
		mutated = true
	}
	switch cfg.Settings.Language {
	case "", "zh-CN", "en":
	default:
		cfg.Settings.Language = ""
		mutated = true
	}
	// BackupRetention 语义：0 = 无限保留（不清理旧快照），因此不能用 "== 0" 判断
	// 字段缺失；只有配置文件里根本没有该字段（旧版本迁移）时才填默认值。
	if !jsonHasKey(data, "settings", "backupRetention") {
		cfg.Settings.BackupRetention = defaults.BackupRetention
		mutated = true
	}
	if cfg.DisabledAgents == nil {
		cfg.DisabledAgents = []string{}
	}
	if cfg.Settings.SkillStorage == "" {
		cfg.Settings.SkillStorage = defaults.SkillStorage
		mutated = true
	}
	if cfg.Settings.SkillSyncMethod == "" {
		cfg.Settings.SkillSyncMethod = defaults.SkillSyncMethod
		mutated = true
	}
	if cfg.Settings.SkillRepos == nil {
		cfg.Settings.SkillRepos = defaults.SkillRepos
		mutated = true
	}
	if cfg.Settings.WindowAction == "" {
		cfg.Settings.WindowAction = defaults.WindowAction
		mutated = true
	}
	if cfg.Settings.WindowAction == "ask" {
		// 旧版本 "ask" 已废弃：默认行为改为 minimize + 不再提醒关闭
		cfg.Settings.WindowAction = "minimize"
		mutated = true
	}
	// 修正越界值后写回磁盘：若不标记 mutated，修正只存在于内存，
	// 每次启动都重新钳制（值合法则不变，无副作用；越界值则永不复原）。
	{
		clamped := ClampLiteDelay(cfg.Settings.LiteAutoDelay)
		if clamped != cfg.Settings.LiteAutoDelay {
			cfg.Settings.LiteAutoDelay = clamped
			mutated = true
		}
	}
	// AutoBackup default: older configs (version 0) predate this field, and bool's
	// zero value cannot distinguish "unset" from "explicit false" — enable on migration.
	if wasOldConfig && !cfg.Settings.AutoBackup {
		cfg.Settings.AutoBackup = true
	}
	// 迁移完成：发生过字段补全/版本升级时写回磁盘，
	// 避免旧 config 文件每次启动都重复走迁移分支。
	if mutated {
		if saveErr := Save(&cfg); saveErr != nil {
			log.Printf("config migration write-back: %v", saveErr)
		}
	}
	return &cfg
}

func nextCorruptPath(path string) string {
	base := fmt.Sprintf("%s.corrupt-%d", path, time.Now().UnixNano())
	for i := 0; i < 100; i++ {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d", base, i)
		}
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
	}
	return fmt.Sprintf("%s-%d", base, os.Getpid())
}

func Save(cfg *AppConfig) error {
	path := configPath()
	if path == "" {
		return errors.New("config: cannot determine config path (home directory unavailable)")
	}
	if cfg == nil {
		return errors.New("config is nil")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// 合并 Load 时提取的未知字段后写回（见 AppConfig.extra 注释）
	if len(cfg.extra) > 0 || len(cfg.settingsExtra) > 0 {
		var m map[string]json.RawMessage
		if uerr := json.Unmarshal(data, &m); uerr == nil {
			for k, v := range cfg.extra {
				m[k] = v
			}
			if len(cfg.settingsExtra) > 0 {
				if settingsRaw, ok := m["settings"]; ok {
					var sm map[string]json.RawMessage
					if uerr := json.Unmarshal(settingsRaw, &sm); uerr == nil {
						for k, v := range cfg.settingsExtra {
							sm[k] = v
						}
						if merged, merr := json.Marshal(sm); merr == nil {
							m["settings"] = merged
						}
					}
				}
			}
			if merged, merr := json.MarshalIndent(m, "", "  "); merr == nil {
				data = merged
			}
		}
	}
	return iowriter.WriteAtomic(path, data, 0600)
}

// extractUnknownKeys 返回 data 顶层中白名单之外的键（原始 JSON 值）。
func extractUnknownKeys(data []byte, known ...string) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	knownSet := make(map[string]bool, len(known))
	for _, k := range known {
		knownSet[k] = true
	}
	out := make(map[string]json.RawMessage)
	for k, v := range m {
		if !knownSet[k] {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// extractUnknownKeysOf 返回 data 中 key 字段对象内的未知键（原始 JSON 值）。
func extractUnknownKeysOf(data []byte, key string, known ...string) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	raw, ok := m[key]
	if !ok {
		return nil
	}
	return extractUnknownKeys(raw, known...)
}

func UserHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func AgentPackDir() string {
	home := UserHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".agentpack")
}
