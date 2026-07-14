package mcp

type Transport string

const (
	TransportStdio Transport = "stdio"
	TransportSSE   Transport = "sse"
	TransportHTTP  Transport = "http"
)

type Server struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	Env         map[string]string `json:"env,omitempty"`
	Cwd         string            `json:"cwd,omitempty"`
	Transport   Transport         `json:"transport"`
	ConfigType  string            `json:"configType,omitempty"`
	URL         string            `json:"url,omitempty"`
	Timeout     int               `json:"timeout,omitempty"`
	Source      string            `json:"source"`
	SourceID    string            `json:"sourceId,omitempty"`
	BoundAgents []string          `json:"boundAgents"`
	InstalledAt string            `json:"installedAt"`
	UpdatedAt   string            `json:"updatedAt"`
}

type McpInstallOptions struct {
	Agents []string          `json:"agents"`
	Env    map[string]string `json:"env"`
	Force  bool              `json:"force"`
}

// ScanItem 表示从 Agent 配置文件扫描到的单个 MCP 服务器
type ScanItem struct {
	Server     Server `json:"server"`
	Managed    bool   `json:"managed"`    // 是否已在 Store 中管理
	AgentID    string `json:"agentId"`
	AgentName  string `json:"agentName"`
	ConfigPath string `json:"configPath"`
}

// ScanResult 表示一次扫描操作的结果
type ScanResult struct {
	Items    []ScanItem `json:"items"`
	Total    int        `json:"total"`
	Managed  int        `json:"managed"`
	NewFound int        `json:"newFound"`
}
