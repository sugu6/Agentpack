package agents

import (
	"path/filepath"
	"runtime"
)

type TraeAdapter struct{}

func NewTraeAdapter() *TraeAdapter { return &TraeAdapter{} }

func (a *TraeAdapter) ID() string                 { return "trae" }
func (a *TraeAdapter) Name() string               { return "TraeCode" }
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
		// Windows 上 Trae 安装目录由用户自选、无标准位置，以注册表
		// （已做 InstallLocation 物理存在校验）为准，故不提供候选路径
		map[string][]string{
			"windows": {},
			"darwin":  {"/Applications/Trae.app", "~/Applications/Trae.app", "/Applications/TraeCode.app", "~/Applications/TraeCode.app"},
			"linux":   {"~/.local/share/applications/trae.desktop", "/usr/share/applications/trae.desktop", "~/.local/share/applications/traecode.desktop", "/usr/share/applications/traecode.desktop"},
		},
		"cn",
	)

	return BuildDetectInfo(hasIDE, false, false, hasConfig, VariantIDE, configPath)
}

// findConfigPath 返回存在的候选配置路径；全部不存在时按平台返回 Trae
// 真实读取的默认路径。
func (a *TraeAdapter) findConfigPath(h string) string {
	candidates := []string{
		filepath.Join(h, "AppData", "Roaming", "Trae", "User", "mcp.json"),
		filepath.Join(h, "Library", "Application Support", "Trae", "User", "mcp.json"),
		filepath.Join(h, ".config", "Trae", "User", "mcp.json"),
		// 更名后新安装的客户端可能使用新品牌目录
		filepath.Join(h, "AppData", "Roaming", "TraeCode", "User", "mcp.json"),
		filepath.Join(h, "Library", "Application Support", "TraeCode", "User", "mcp.json"),
		filepath.Join(h, ".config", "TraeCode", "User", "mcp.json"),
		filepath.Join(h, ".trae", "mcp.json"),
	}
	if found := FirstExistingFile(candidates); found != "" {
		return found
	}
	// 没有任何候选文件存在时，按平台返回 Trae 实际读取的真实配置路径：
	// ~/.trae/mcp.json 是 AgentPack 自造路径，Trae 不会加载——新装
	// （从未运行过、无配置目录）的用户在此写入 MCP 配置后 Trae 完全
	// 感知不到，功能静默失效。
	if runtime.GOOS == "darwin" {
		return candidates[1]
	}
	if runtime.GOOS == "linux" {
		return candidates[2]
	}
	return candidates[0]
}
