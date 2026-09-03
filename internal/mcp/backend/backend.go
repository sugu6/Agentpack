// Package backend 定义 MCP 配置文件的读写后端抽象（接口）与确定性 ID 生成。
// 具体实现见 jsonbackend / tomlbackend 子包；工厂 NewBackend 在主包（store 侧），
// 以避免 backend ↔ 实现包的循环依赖。
package backend

import (
	"crypto/sha256"
	"encoding/hex"

	"agentpack/internal/mcp/types"
)

type ConfigReader interface {
	Read(path string) (map[string]types.Server, error)
}

type ConfigWriter interface {
	Write(path string, servers map[string]types.Server) error
}

type Backend interface {
	ConfigReader
	ConfigWriter
	BackupDir() string
}

// ServerDeterministicID 生成配置文件中服务器的确定性 ID。
// 格式为 name@<path 短哈希>：保持确定性以便跨重启与去重（matchManagedID 依赖），
// 同时避免把完整配置文件路径（含用户目录名）写入 ID 并暴露给前端/数据库。
// 短哈希取 sha256 前 4 字节（8 位十六进制），不同路径碰撞概率可忽略。
func ServerDeterministicID(name, path string) string {
	sum := sha256.Sum256([]byte(path))
	return name + "@" + hex.EncodeToString(sum[:4])
}
