package agents

type AgentType string

const (
	TypeClaudeCode AgentType = "claude-code"
	TypeOpenCode   AgentType = "opencode"
	TypeCursor     AgentType = "cursor"
	TypeCodex      AgentType = "codex"
	TypeTrae       AgentType = "trae"
	TypeTraeCN     AgentType = "trae-cn"
)

// AgentVariant 表示 Agent 的检测变体类型
type AgentVariant string

const (
	VariantCLI     AgentVariant = "cli"
	VariantDesktop AgentVariant = "desktop"
	VariantIDE     AgentVariant = "ide"
	VariantConfig  AgentVariant = "config" // 仅发现配置文件
)

type AgentStatus string

const (
	StatusDetected AgentStatus = "detected"
	StatusEnabled  AgentStatus = "enabled"
	StatusDisabled AgentStatus = "disabled"
	StatusNotFound AgentStatus = "not_found"
	StatusError    AgentStatus = "error"
)

type ConfigFormat string

const (
	FormatJSON ConfigFormat = "json"
	FormatTOML ConfigFormat = "toml"
)

type Agent struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Type          AgentType    `json:"type"`
	Variant       AgentVariant `json:"variant"`
	Status        AgentStatus  `json:"status"`
	ConfigPath    string       `json:"configPath"`
	ConfigFormat  ConfigFormat `json:"configFormat"`
	McpCount      int          `json:"mcpCount"`
	DetectedAt    string       `json:"detectedAt"`
	LastScannedAt string       `json:"lastScannedAt"`
	Error         string       `json:"error,omitempty"`
}

// DisplayName 返回带变体信息的显示名称
func (a *Agent) DisplayName() string {
	switch a.Variant {
	case VariantCLI:
		return a.Name + " CLI"
	case VariantDesktop:
		return a.Name + " Desktop"
	case VariantIDE:
		return a.Name + " IDE"
	default:
		return a.Name
	}
}

type Adapter interface {
	ID() string
	Name() string
	Type() AgentType
	ConfigFormat() ConfigFormat
	Detect() *DetectInfo
	SkillsDir() string
}

type DetectInfo struct {
	Status     AgentStatus
	Variant    AgentVariant
	ConfigPath string
	Error      string
}
