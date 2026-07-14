package agents

import (
	"path/filepath"
)

type TraeCNAdapter struct{}

func NewTraeCNAdapter() *TraeCNAdapter { return &TraeCNAdapter{} }

func (a *TraeCNAdapter) ID() string                 { return "trae-cn" }
func (a *TraeCNAdapter) Name() string               { return "Trae CN" }
func (a *TraeCNAdapter) Type() AgentType            { return TypeTraeCN }
func (a *TraeCNAdapter) ConfigFormat() ConfigFormat { return FormatJSON }

func (a *TraeCNAdapter) SkillsDir() string {
	return filepath.Join(homeDir(), ".trae-cn", "skills")
}

func (a *TraeCNAdapter) Detect() *DetectInfo {
	h := homeDir()
	if h == "" {
		return &DetectInfo{Status: StatusNotFound, Variant: VariantIDE}
	}

	configPath := a.findConfigPath(h)
	hasConfig := configPath != "" && fileExists(configPath)

	hasIDE := DetectIDE(
		[]string{"Trae CN", "TraeCN"},
		map[string][]string{
			"windows": {"Trae CN", "TraeCN", "Trae CN/User", "TraeCN/User"},
			"darwin":  {"Trae CN", "com.trae.cn.app"},
			"linux":   {"Trae CN", "TraeCN"},
		},
	)

	return BuildDetectInfo(hasIDE, false, false, hasConfig, VariantIDE, configPath)
}

func (a *TraeCNAdapter) findConfigPath(h string) string {
	candidates := []string{
		filepath.Join(h, "AppData", "Roaming", "Trae CN", "User", "mcp.json"),
		filepath.Join(h, "AppData", "Roaming", "TraeCN", "User", "mcp.json"),
		filepath.Join(h, "Library", "Application Support", "Trae CN", "User", "mcp.json"),
		filepath.Join(h, ".config", "Trae CN", "User", "mcp.json"),
		filepath.Join(h, ".trae-cn", "mcp.json"),
	}
	if found := FirstExistingFile(candidates); found != "" {
		return found
	}
	// 没有任何候选文件存在时，返回默认路径
	return candidates[len(candidates)-1]
}
