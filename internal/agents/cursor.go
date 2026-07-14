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
		map[string][]string{
			"windows": {"Cursor", "Cursor/User"},
			"darwin":  {"Cursor", "com.cursor.app"},
			"linux":   {"Cursor"},
		},
	)

	return BuildDetectInfo(hasIDE, false, false, hasConfig, VariantIDE, configPath)
}
