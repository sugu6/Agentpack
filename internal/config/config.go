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

const currentVersion = 1

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
	WindowAction    string                  `json:"windowAction"`    // "ask" | "minimize" | "exit"
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
			"official":  {Enabled: true},
			"github":    {Enabled: true},
			"skills-sh": {Enabled: true},
			"smithery":  {Enabled: true},
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
		WindowAction: "ask",
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
			setLastLoadError(fmt.Errorf("config corrupted (parse=%v, quarantine failed: %v)", err, rerr))
		} else {
			log.Printf("config parse failed, original file moved to %s: %v", quarantine, err)
			setLastLoadError(fmt.Errorf("config corrupted, original moved to %s: %w", filepath.Base(quarantine), err))
		}
		return Default()
	}
	wasOldConfig := cfg.Version == 0
	if wasOldConfig {
		cfg.Version = currentVersion
	}
	defaults := DefaultSettings()
	if cfg.Settings.Theme == "" {
		cfg.Settings.Theme = defaults.Theme
	}
	// MarketSources: 若整体为 nil 直接用默认值；否则逐个 key 补全缺失项
	// 避免旧 config 文件缺少新增来源（如 smithery）时被误判为禁用
	if cfg.Settings.MarketSources == nil {
		cfg.Settings.MarketSources = defaults.MarketSources
	} else {
		for key, def := range defaults.MarketSources {
			if _, exists := cfg.Settings.MarketSources[key]; !exists {
				cfg.Settings.MarketSources[key] = def
			}
		}
	}
	if cfg.Settings.BackupCount == 0 {
		cfg.Settings.BackupCount = defaults.BackupCount
	}
	if cfg.Settings.BackupRetention == 0 {
		cfg.Settings.BackupRetention = defaults.BackupRetention
	}
	if cfg.DisabledAgents == nil {
		cfg.DisabledAgents = []string{}
	}
	if cfg.Settings.SkillStorage == "" {
		cfg.Settings.SkillStorage = defaults.SkillStorage
	}
	if cfg.Settings.SkillSyncMethod == "" {
		cfg.Settings.SkillSyncMethod = defaults.SkillSyncMethod
	}
	if cfg.Settings.SkillRepos == nil {
		cfg.Settings.SkillRepos = defaults.SkillRepos
	}
	if cfg.Settings.WindowAction == "" {
		cfg.Settings.WindowAction = defaults.WindowAction
	}
	// AutoBackup default: older configs (version 0) predate this field, and bool's
	// zero value cannot distinguish "unset" from "explicit false" — enable on migration.
	if wasOldConfig && !cfg.Settings.AutoBackup {
		cfg.Settings.AutoBackup = true
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
	return iowriter.WriteAtomic(path, data, 0600)
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
