package agents

import (
	"path/filepath"
)

type CodexAdapter struct{}

func NewCodexAdapter() *CodexAdapter { return &CodexAdapter{} }

func (a *CodexAdapter) ID() string                 { return "codex" }
func (a *CodexAdapter) Name() string               { return "Codex" }
func (a *CodexAdapter) Type() AgentType            { return TypeCodex }
func (a *CodexAdapter) ConfigFormat() ConfigFormat { return FormatTOML }

func (a *CodexAdapter) SkillsDir() string {
	return filepath.Join(homeDir(), ".codex", "skills")
}

func (a *CodexAdapter) Detect() *DetectInfo {
	h := homeDir()
	if h == "" {
		return &DetectInfo{Status: StatusNotFound, Variant: VariantCLI}
	}

	configPath := filepath.Join(h, ".codex", "config.toml")
	hasConfig := fileExists(configPath)

	hasCLI := DetectCLI("@openai/codex", "codex")

	return BuildDetectInfo(false, hasCLI, false, hasConfig, VariantCLI, configPath)
}

// CodexDesktopAdapter 检测微软商店版 Codex 桌面端（UWP 包 OpenAI.Codex）。
// 与 CLI 共享 ~/.codex 配置与 skills 目录，但二进制分发方式不同。
type CodexDesktopAdapter struct{}

func NewCodexDesktopAdapter() *CodexDesktopAdapter { return &CodexDesktopAdapter{} }

func (a *CodexDesktopAdapter) ID() string                 { return "codex-desktop" }
func (a *CodexDesktopAdapter) Name() string               { return "Codex" }
func (a *CodexDesktopAdapter) Type() AgentType            { return TypeCodex }
func (a *CodexDesktopAdapter) ConfigFormat() ConfigFormat { return FormatTOML }

func (a *CodexDesktopAdapter) SkillsDir() string {
	return filepath.Join(homeDir(), ".codex", "skills")
}

func (a *CodexDesktopAdapter) Detect() *DetectInfo {
	h := homeDir()
	if h == "" {
		return &DetectInfo{Status: StatusNotFound, Variant: VariantDesktop}
	}

	configPath := filepath.Join(h, ".codex", "config.toml")

	// 微软商店 UWP 应用不会出现在传统 Uninstall 注册表中，
	// 需通过 AppModel\Repository\Packages 注册表键检测。
	hasDesktop := CheckAppxPackageInstalled("OpenAI.Codex")

	return BuildDetectInfo(false, false, hasDesktop, false, VariantDesktop, configPath)
}
