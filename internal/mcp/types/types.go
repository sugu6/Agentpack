package types

import (
	"errors"
	"fmt"
	"strings"
)

// ErrPartialRead 表示配置文件中部分条目无法解析（如危险命令），返回的条目集合可用但不完整。
// 只读路径（Load/Scan）应保留已解析条目；写路径（write/remove）必须拒绝，
// 否则整表重写会把未解析的条目静默删除。
var ErrPartialRead = errors.New("config contains unparsable entries that would be lost on rewrite")

// ValidateCommand checks that a command doesn't contain dangerous shell metacharacters
// or path traversal patterns. Commands should be simple executable names or absolute paths.
func ValidateCommand(cmd string) error {
	if cmd == "" {
		return fmt.Errorf("command required for stdio transport")
	}
	// 检查危险 shell 元字符。
	// 只封锁真正危险的字符，允许 ( ) { } ! ~ 等合法路径/包名字符通过。
	// 参数（Args）由 exec 系列函数直接传递，不经过 shell 解释，因而不受此限制。
	dangerous := []string{";", "|", "&", ">", "<", "$", "`", "\n", "\r"}
	for _, ch := range dangerous {
		if strings.Contains(cmd, ch) {
			return fmt.Errorf("command contains dangerous shell metacharacter %q: commands should be a single executable name or path", ch)
		}
	}
	// 检查路径遍历
	if strings.Contains(cmd, "..") {
		return fmt.Errorf("command contains path traversal pattern '..'")
	}
	return nil
}

type Transport string

const (
	TransportStdio          Transport = "stdio"
	TransportSSE            Transport = "sse"
	TransportHTTP           Transport = "http"
	TransportStreamableHTTP Transport = "streamable-http"
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
	// Headers 透传远程服务器（SSE/HTTP/streamable-http）的认证/自定义请求头，
	// 读写回写时原样保留。此前该字段在解析时被丢弃，任何一次写文件都会
	// 让需要鉴权的远程服务器失去 headers。
	Headers map[string]string `json:"headers,omitempty"`
	// Enabled 透传 opencode 的 "enabled" 状态（用户手动禁用的服务器）。
	// 缺失时 opencode 默认视为启用，因此不保留会导致禁用被写回后撤销。
	Enabled *bool `json:"enabled,omitempty"`
	Timeout int   `json:"timeout,omitempty"`
	// Extra 保存配置文件条目中未被显式建模的原始键值（如 Codex 应用自管理的
	// env_vars / startup_timeout_sec / oauth 等最新字段）。读文件时收集、写文件时
	// 原样回写，保证整表重写不丢失这些键。不参与 DB 持久化与前端展示。
	Extra       map[string]any `json:"-"`
	Source      string         `json:"source"`
	SourceID    string         `json:"sourceId,omitempty"`
	BoundAgents []string       `json:"boundAgents"`
	InstalledAt string         `json:"installedAt"`
	UpdatedAt   string         `json:"updatedAt"`
}

type McpInstallOptions struct {
	Agents []string          `json:"agents"`
	Env    map[string]string `json:"env"`
	Force  bool              `json:"force"`
}

// ScanSource 表示扫描到的 MCP 服务器的来源 agent 信息。
// 同一个服务器可能同时存在于多个 agent 的配置文件中（例如 Claude Code 和 OpenCode 都装了 context7），
// 这种情况下该服务器会合并为一个 ScanItem，但 Sources 中保留所有来源信息。
type ScanSource struct {
	AgentID    string `json:"agentId"`
	AgentName  string `json:"agentName"`
	ConfigPath string `json:"configPath"`
}

// ScanItem 表示从 Agent 配置文件扫描到的单个 MCP 服务器
type ScanItem struct {
	Server     Server       `json:"server"`
	Managed    bool         `json:"managed"`    // 是否已在 Store 中管理（已管理的不再展示为"新发现"）
	AgentID    string       `json:"agentId"`    // 主来源（向后兼容），取 Sources[0]
	AgentName  string       `json:"agentName"`  // 主来源（向后兼容），取 Sources[0]
	ConfigPath string       `json:"configPath"` // 主来源（向后兼容），取 Sources[0]
	Sources    []ScanSource `json:"sources"`    // 所有来源 agent（按发现顺序）
}

// ScanResult 表示一次扫描操作的结果
type ScanResult struct {
	Items    []ScanItem `json:"items"`
	Total    int        `json:"total"`
	Managed  int        `json:"managed"`
	NewFound int        `json:"newFound"`
	Failed   int        `json:"failed"` // 无法读取的配置文件数（其服务器未显示）
}
