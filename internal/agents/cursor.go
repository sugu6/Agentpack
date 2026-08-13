package agents

import (
	"path/filepath"
)

type CursorAdapter struct{}

func NewCursorAdapter() *CursorAdapter { return &CursorAdapter{} }

func (a *CursorAdapter) ID() string                 { return "cursor" }
func (a *CursorAdapter) Name() string               { return "Cursor" }
func (a *CursorAdapter) Type() AgentType            { return TypeCursor }
func (a *CursorAdapter) ConfigFormat() ConfigFormat { return FormatJSON }

func (a *CursorAdapter) SkillsDir() string {
	return filepath.Join(homeDir(), ".cursor", "skills")
}

func (a *CursorAdapter) Detect() *DetectInfo {
	h := homeDir()
	if h == "" {
		return &DetectInfo{Status: StatusNotFound, Variant: VariantIDE}
	}

	configPath := filepath.Join(h, ".cursor", "mcp.json")
	hasConfig := fileExists(configPath)

	hasIDE := DetectIDE(
		[]string{"Cursor", "Cursor Editor"},
		// 安装位置候选；配置目录（%APPDATA%\Cursor 等）不作为已安装证据，
		// 卸载后残留的配置目录会把已卸载的 Cursor 误判为已安装
		map[string][]string{
			"windows": {
				`%LOCALAPPDATA%\Programs\cursor\Cursor.exe`,
				`%LOCALAPPDATA%\Programs\Cursor\Cursor.exe`,
				`%ProgramFiles%\Cursor\Cursor.exe`,
			},
			"darwin": {"/Applications/Cursor.app", "~/Applications/Cursor.app"},
			"linux": {
				"~/.local/share/applications/cursor.desktop",
				"/usr/share/applications/cursor.desktop",
				"/var/lib/snapd/desktop/applications/cursor_cursor.desktop",
			},
		},
	)

	return BuildDetectInfo(hasIDE, false, false, hasConfig, VariantIDE, configPath)
}
