package mcp

import (
	"agentpack/internal/iowriter"
	"crypto/sha256"
	"encoding/hex"
	"os"
)

type ConfigReader interface {
	Read(path string) (map[string]Server, error)
}

type ConfigWriter interface {
	Write(path string, servers map[string]Server) error
}

type Backend interface {
	ConfigReader
	ConfigWriter
	BackupDir() string
}

func NewBackend(agentType string) Backend {
	switch agentType {
	case "codex":
		return NewTomlBackend()
	default:
		return NewJsonBackend(agentType)
	}
}

func BackupPath(agentType string) string {
	return NewBackend(agentType).BackupDir()
}

func BackupConfig(agentType, path string) (string, error) {
	return iowriter.BackupFile(path, BackupPath(agentType))
}

func BackupConfigContent(agentType, path string) (string, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	backupPath, err := iowriter.BackupFile(path, BackupPath(agentType))
	if err != nil {
		return "", nil, err
	}
	return backupPath, data, nil
}

// serverDeterministicID 生成配置文件中服务器的确定性 ID。
// 格式为 name@<path 短哈希>：保持确定性以便跨重启与去重（matchManagedID 依赖），
// 同时避免把完整配置文件路径（含用户目录名）写入 ID 并暴露给前端/数据库。
// 短哈希取 sha256 前 4 字节（8 位十六进制），不同路径碰撞概率可忽略。
func serverDeterministicID(name, path string) string {
	sum := sha256.Sum256([]byte(path))
	return name + "@" + hex.EncodeToString(sum[:4])
}
