package agents

import (
	"path/filepath"
)

type ClaudeCodeAdapter struct{}

func NewClaudeCodeAdapter() *ClaudeCodeAdapter { return &ClaudeCodeAdapter{} }

func (a *ClaudeCodeAdapter) ID() string                 { return "claude-code" }
func (a *ClaudeCodeAdapter) Name() string               { return "Claude Code" }
func (a *ClaudeCodeAdapter) Type() AgentType            { return TypeClaudeCode }
func (a *ClaudeCodeAdapter) ConfigFormat() ConfigFormat { return FormatJSON }

func (a *ClaudeCodeAdapter) SkillsDir() string {
	return filepath.Join(homeDir(), ".claude", "skills")
}

func (a *ClaudeCodeAdapter) Detect() *DetectInfo {
	h := homeDir()
	if h == "" {
		return &DetectInfo{Status: StatusNotFound, Variant: VariantCLI}
	}

	configPath := filepath.Join(h, ".claude.json")
	hasConfig := fileExists(configPath)

	hasCLI := DetectCLI("@anthropic-ai/claude-code", "claude", "claude-code")

	return BuildDetectInfo(false, hasCLI, false, hasConfig, VariantCLI, configPath)
}

type ClaudeCodeDesktopAdapter struct{}

func NewClaudeCodeDesktopAdapter() *ClaudeCodeDesktopAdapter { return &ClaudeCodeDesktopAdapter{} }

func (a *ClaudeCodeDesktopAdapter) ID() string                 { return "claude-code-desktop" }
func (a *ClaudeCodeDesktopAdapter) Name() string               { return "Claude Code" }
func (a *ClaudeCodeDesktopAdapter) Type() AgentType            { return TypeClaudeCode }
func (a *ClaudeCodeDesktopAdapter) ConfigFormat() ConfigFormat { return FormatJSON }

func (a *ClaudeCodeDesktopAdapter) SkillsDir() string {
	return filepath.Join(homeDir(), ".claude", "skills")
}

func (a *ClaudeCodeDesktopAdapter) Detect() *DetectInfo {
	h := homeDir()
	if h == "" {
		return &DetectInfo{Status: StatusNotFound, Variant: VariantDesktop}
	}

	configPath := filepath.Join(h, ".claude.json")

	hasDesktop := CheckAppInstalledViaRegistry([]string{"Claude Desktop", "Anthropic Claude"})

	return BuildDetectInfo(false, false, hasDesktop, false, VariantDesktop, configPath)
}
