// Package mcp 对外导出 MCP 管理与类型。实现按功能拆分为子包：
//
//	types/      Server / Transport / Scan 结果 / ValidateCommand / ErrPartialRead（共享数据与校验）
//	backend/    Backend 接口 + ServerDeterministicID
//	jsonbackend/ JSON 配置后端（Claude Code / Cursor / OpenCode / Trae）
//	tomlbackend/ TOML 配置后端（Codex）
//	store/      Store 主逻辑 + 后端工厂 NewBackend
//
// 本包通过类型别名 re-export 子包类型，使外部引用（app.go / internal/backup 等）
// 继续使用 mcp.Server / mcp.Transport / mcp.Store 等，无需改动。
package mcp

import (
	"agentpack/internal/mcp/backend"
	"agentpack/internal/mcp/store"
	"agentpack/internal/mcp/types"
)

// 类型别名：保持对外 API 稳定。
type (
	Server            = types.Server
	Transport         = types.Transport
	McpInstallOptions = types.McpInstallOptions
	ScanSource        = types.ScanSource
	ScanItem          = types.ScanItem
	ScanResult        = types.ScanResult
	Store             = store.Store
	MutationHandler   = store.MutationHandler
	MutationFunc      = store.MutationFunc
	MutationDetail    = store.MutationDetail
)

// 常量别名：Transport 值。
const (
	TransportStdio          = types.TransportStdio
	TransportSSE            = types.TransportSSE
	TransportHTTP           = types.TransportHTTP
	TransportStreamableHTTP = types.TransportStreamableHTTP
)

// ErrDuplicateServer 表示 Add/Update 时发现归一化 key（命令/参数或 URL）相同的服务器已存在。
var ErrDuplicateServer = store.ErrDuplicateServer

// ErrPartialRead 表示配置包含无法解析的条目，重写时会丢失。
var ErrPartialRead = types.ErrPartialRead

// NewStore 创建 MCP Store。
func NewStore() *Store { return store.NewStore() }

// NewBackend 根据 agent 类型返回对应配置文件后端。
func NewBackend(agentType string) backend.Backend {
	return store.NewBackend(agentType)
}

// ValidateCommand checks that a command doesn't contain dangerous shell metacharacters.
func ValidateCommand(cmd string) error {
	return types.ValidateCommand(cmd)
}
