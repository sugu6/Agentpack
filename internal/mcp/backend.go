package mcp

import (
	"agentpack/internal/iowriter"
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
