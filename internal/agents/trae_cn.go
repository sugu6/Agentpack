package agents

import (
	"path/filepath"
)

type TraeCNAdapter struct{}

func NewTraeCNAdapter() *TraeCNAdapter { return &TraeCNAdapter{} }

func (a *TraeCNAdapter) ID() string                 { return "trae-cn" }
func (a *TraeCNAdapter) Name() string               { return "TraeCode CN" }
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
		// 国内版 Trae 产品线：既有旧名 "Trae CN"/"TraeCN"，也有新品牌
		// "TraeCode CN"（注册表 DisplayName 形如 "TraeCode CN (User)"）。
		// 检测用 Contains 子串匹配，必须显式列出，否则 "traecode cn" 不会命中 "trae cn"。
		[]string{"Trae CN", "TraeCN", "TraeCode CN"},
		// Windows 上安装目录由用户自选，以注册表（含物理存在校验）为准
		map[string][]string{
			"windows": {},
			"darwin":  {"/Applications/Trae CN.app", "~/Applications/Trae CN.app", "/Applications/TraeCN.app", "/Applications/TraeCode CN.app"},
			"linux":   {"~/.local/share/applications/trae-cn.desktop", "/usr/share/applications/trae-cn.desktop", "~/.local/share/applications/traecode-cn.desktop"},
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
		// 更名后新安装的客户端可能使用新品牌目录（TraeCode CN）
		filepath.Join(h, "AppData", "Roaming", "TraeCode CN", "User", "mcp.json"),
		filepath.Join(h, "Library", "Application Support", "TraeCode CN", "User", "mcp.json"),
		filepath.Join(h, ".config", "TraeCode CN", "User", "mcp.json"),
		filepath.Join(h, ".trae-cn", "mcp.json"),
	}
	if found := FirstExistingFile(candidates); found != "" {
		return found
	}
	// 没有任何候选文件存在时，返回默认路径
	return candidates[len(candidates)-1]
}
