package agents

import (
	"path/filepath"
)

type OpenCodeAdapter struct{}

func NewOpenCodeAdapter() *OpenCodeAdapter { return &OpenCodeAdapter{} }

func (a *OpenCodeAdapter) ID() string                 { return "opencode" }
func (a *OpenCodeAdapter) Name() string               { return "OpenCode" }
func (a *OpenCodeAdapter) Type() AgentType            { return TypeOpenCode }
func (a *OpenCodeAdapter) ConfigFormat() ConfigFormat { return FormatJSON }

func (a *OpenCodeAdapter) SkillsDir() string {
	return filepath.Join(homeDir(), ".config", "opencode", "skills")
}

func (a *OpenCodeAdapter) Detect() *DetectInfo {
	h := homeDir()
	if h == "" {
		return &DetectInfo{Status: StatusNotFound, Variant: VariantCLI}
	}

	configPath := filepath.Join(h, ".config", "opencode", "opencode.json")
	hasConfig := fileExists(configPath)

	hasCLI := DetectCLI("opencode-ai", "opencode")

	return BuildDetectInfo(false, hasCLI, false, hasConfig, VariantCLI, configPath)
}

type OpenCodeDesktopAdapter struct{}

func NewOpenCodeDesktopAdapter() *OpenCodeDesktopAdapter { return &OpenCodeDesktopAdapter{} }

func (a *OpenCodeDesktopAdapter) ID() string                 { return "opencode-desktop" }
func (a *OpenCodeDesktopAdapter) Name() string               { return "OpenCode" }
func (a *OpenCodeDesktopAdapter) Type() AgentType            { return TypeOpenCode }
func (a *OpenCodeDesktopAdapter) ConfigFormat() ConfigFormat { return FormatJSON }

func (a *OpenCodeDesktopAdapter) SkillsDir() string {
	return filepath.Join(homeDir(), ".config", "opencode", "skills")
}

func (a *OpenCodeDesktopAdapter) Detect() *DetectInfo {
	h := homeDir()
	if h == "" {
		return &DetectInfo{Status: StatusNotFound, Variant: VariantDesktop}
	}

	configPath := filepath.Join(h, ".config", "opencode", "opencode.json")

	hasDesktop := CheckAppInstalledViaRegistry([]string{"OpenCode"})

	return BuildDetectInfo(false, false, hasDesktop, false, VariantDesktop, configPath)
}
