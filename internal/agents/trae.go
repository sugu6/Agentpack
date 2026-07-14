package agents

import (
	"path/filepath"
)

type TraeAdapter struct{}

func NewTraeAdapter() *TraeAdapter { return &TraeAdapter{} }

func (a *TraeAdapter) ID() string                 { return "trae" }
func (a *TraeAdapter) Name() string               { return "Trae" }
func (a *TraeAdapter) Type() AgentType            { return TypeTrae }
func (a *TraeAdapter) ConfigFormat() ConfigFormat { return FormatJSON }

func (a *TraeAdapter) SkillsDir() string {
	return filepath.Join(homeDir(), ".trae", "skills")
}

func (a *TraeAdapter) Detect() *DetectInfo {
	h := homeDir()
	if h == "" {
		return &DetectInfo{Status: StatusNotFound, Variant: VariantIDE}
	}

	configPath := a.findConfigPath(h)
	hasConfig := configPath != "" && fileExists(configPath)

	hasIDE := DetectIDE(
		[]string{"Trae", "Trae IDE"},
		map[string][]string{
			"windows": {"Trae", "Trae/User"},
			"darwin":  {"Trae", "com.trae.app"},
			"linux":   {"Trae"},
		},
		"cn",
	)

	return BuildDetectInfo(hasIDE, false, false, hasConfig, VariantIDE, configPath)
}

func (a *TraeAdapter) findConfigPath(h string) string {
	candidates := []string{
		filepath.Join(h, "AppData", "Roaming", "Trae", "User", "mcp.json"),
		filepath.Join(h, "Library", "Application Support", "Trae", "User", "mcp.json"),
		filepath.Join(h, ".config", "Trae", "User", "mcp.json"),
		filepath.Join(h, ".trae", "mcp.json"),
	}
	if found := FirstExistingFile(candidates); found != "" {
		return found
	}
	// 没有任何候选文件存在时，返回第一个候选路径作为默认配置路径，
	// 确保 MCP Store 知道写入位置。AgentPack 会在首次写入时创建此文件。
	return candidates[len(candidates)-1]
}
