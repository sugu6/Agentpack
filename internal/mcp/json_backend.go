package mcp

import (
	"agentpack/internal/config"
	"agentpack/internal/iowriter"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type JsonBackend struct {
	agentType string
}

func NewJsonBackend(agentType string) *JsonBackend {
	return &JsonBackend{agentType: agentType}
}

func (b *JsonBackend) BackupDir() string {
	return filepath.Join(config.AgentPackDir(), "backups", "mcp")
}

func (b *JsonBackend) isOpencode() bool {
	return b.agentType == "opencode"
}

func (b *JsonBackend) Read(path string) (map[string]Server, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Server{}, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return map[string]Server{}, nil
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	// Try top-level "mcpServers" (Claude Code, Cursor)
	var raw json.RawMessage
	if r, ok := cfg["mcpServers"]; ok {
		raw = r
	} else if r, ok := cfg["servers"]; ok {
		// Try top-level "servers" (workspace .vscode/mcp.json, .trae/mcp.json)
		raw = r
	} else {
		// Try nested "mcp" →"servers" or flat "mcp": { "name": {...} } (OpenCode)
		if mcpRaw, ok2 := cfg["mcp"]; ok2 {
			var mcp map[string]json.RawMessage
			if err := json.Unmarshal(mcpRaw, &mcp); err == nil {
				if srvRaw, ok3 := mcp["servers"]; ok3 {
					raw = srvRaw
				} else if b.isOpencode() {
					raw = mcpRaw
				}
			}
		}
	}
	if len(raw) == 0 {
		return map[string]Server{}, nil
	}

	var mcServers map[string]json.RawMessage
	if err := json.Unmarshal(raw, &mcServers); err != nil {
		return nil, fmt.Errorf("parse mcpServers in %s: %w", path, err)
	}

	out := make(map[string]Server, len(mcServers))
	for name, raw := range mcServers {
		s, err := b.parseJsonServer(name, raw)
		if err != nil {
			return nil, err
		}
		s.Source = "config"
		if s.ID == "" {
			s.ID = name + "@" + path
		}
		out[name] = s
	}
	return out, nil
}

// jsonServer is the standard JSON format for Claude Code, Cursor, VS Code
type jsonServer struct {
	Command    string            `json:"command"`
	Args       []string          `json:"args"`
	Env        map[string]string `json:"env,omitempty"`
	Transport  string            `json:"transport,omitempty"`
	URL        string            `json:"url,omitempty"`
	Timeout    int               `json:"timeout,omitempty"`
	WorkingDir string            `json:"cwd,omitempty"`
	Type       string            `json:"type,omitempty"`
}

// opencodeServer is the OpenCode-specific JSON format where command is an array
type opencodeServer struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command"`
	Environment map[string]string `json:"environment"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers"`
	Enabled     *bool             `json:"enabled,omitempty"`
	Timeout     int               `json:"timeout"`
}

func (b *JsonBackend) parseJsonServer(name string, raw json.RawMessage) (Server, error) {
	if b.isOpencode() {
		return b.parseOpencodeServer(name, raw)
	}
	return b.parseStandardJsonServer(name, raw)
}

func (b *JsonBackend) parseStandardJsonServer(name string, raw json.RawMessage) (Server, error) {
	var js jsonServer
	if err := json.Unmarshal(raw, &js); err != nil {
		return Server{}, err
	}
	// 验证命令不包含危险的 shell 元字符（与TomlBackend 和Store.Add/Update 保持一致）
	if js.Command != "" {
		if err := ValidateCommand(js.Command); err != nil {
			return Server{}, fmt.Errorf("server %q: %w", name, err)
		}
	}
	transport := TransportStdio
	if js.Transport == "sse" {
		transport = TransportSSE
	} else if js.Transport == "http" || js.Type == "http" {
		transport = TransportHTTP
	}
	// 归一化：空切片视为 nil，保证读写一致性
	args := js.Args
	if len(args) == 0 {
		args = nil
	}
	return Server{
		Name:       name,
		Command:    js.Command,
		Args:       args,
		Env:        js.Env,
		Transport:  transport,
		ConfigType: js.Type,
		URL:        js.URL,
		Timeout:    js.Timeout,
		Cwd:        js.WorkingDir,
		Source:     "config",
	}, nil
}

func (b *JsonBackend) parseOpencodeServer(name string, raw json.RawMessage) (Server, error) {
	var oc opencodeServer
	if err := json.Unmarshal(raw, &oc); err != nil {
		// Fallback: try standard format in case user manually edited
		return b.parseStandardJsonServer(name, raw)
	}

	var command string
	var args []string
	if len(oc.Command) > 0 {
		command = oc.Command[0]
		if len(oc.Command) > 1 {
			args = oc.Command[1:]
		}
	}
	// 验证命令不包含危险的 shell 元字符）
	if command != "" {
		if err := ValidateCommand(command); err != nil {
			return Server{}, fmt.Errorf("server %q: %w", name, err)
		}
	}

	transport := TransportStdio
	if oc.Type == "remote" {
		transport = TransportHTTP
	}

	// 归一化：空切片视为 nil
	if len(args) == 0 {
		args = nil
	}

	return Server{
		Name:       name,
		Command:    command,
		Args:       args,
		Env:        oc.Environment,
		Transport:  transport,
		ConfigType: oc.Type,
		URL:        oc.URL,
		Timeout:    oc.Timeout,
		Source:     "config",
	}, nil
}

func (b *JsonBackend) Write(path string, servers map[string]Server) error {
	if b.isOpencode() {
		return b.writeOpencode(path, servers)
	}
	return b.writeStandard(path, servers)
}

func (b *JsonBackend) writeStandard(path string, servers map[string]Server) error {
	// Read existing config to preserve non-mcpServers fields
	existing := make(map[string]json.RawMessage)
	data, err := os.ReadFile(path)
	if err == nil && len(data) > 0 {
		if uerr := json.Unmarshal(data, &existing); uerr != nil {
			return fmt.Errorf("refuse to overwrite %s: existing file is not valid JSON: %w", path, uerr)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read existing %s: %w", path, err)
	}

	mcServers := make(map[string]json.RawMessage, len(servers))
	for name, s := range servers {
		js := jsonServer{
			Command:    s.Command,
			Args:       s.Args,
			Env:        s.Env,
			Type:       s.ConfigType,
			URL:        s.URL,
			Timeout:    s.Timeout,
			WorkingDir: s.Cwd,
		}
		// 如果原始配置没有 type 字段，根据transport 推导
		if js.Type == "" {
			js.Type = string(s.Transport)
		}
		encoded, err := json.Marshal(js)
		if err != nil {
			return err
		}
		mcServers[name] = encoded
	}

	// Update only mcpServers in the existing config
	mcServersRaw, err := json.Marshal(mcServers)
	if err != nil {
		return err
	}
	existing["mcpServers"] = mcServersRaw

	out, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return err
	}
	return iowriter.WriteAtomic(path, out, 0600)
}

func (b *JsonBackend) writeOpencode(path string, servers map[string]Server) error {
	existing := make(map[string]json.RawMessage)
	data, err := os.ReadFile(path)
	if err == nil && len(data) > 0 {
		if uerr := json.Unmarshal(data, &existing); uerr != nil {
			return fmt.Errorf("refuse to overwrite %s: existing file is not valid JSON: %w", path, uerr)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read existing %s: %w", path, err)
	}

	opencodeServers := make(map[string]json.RawMessage, len(servers))
	for name, s := range servers {
		oc := b.serverToOpencode(s)
		encoded, err := json.Marshal(oc)
		if err != nil {
			return err
		}
		opencodeServers[name] = encoded
	}

	serversRaw, err := json.Marshal(opencodeServers)
	if err != nil {
		return err
	}

	if b.detectFlatMcpFormat(existing) {
		existing["mcp"] = serversRaw
	} else {
		var mcpObj map[string]json.RawMessage
		if mcpRaw, ok := existing["mcp"]; ok {
			_ = json.Unmarshal(mcpRaw, &mcpObj)
		}
		if mcpObj == nil {
			mcpObj = make(map[string]json.RawMessage)
		}
		mcpObj["servers"] = serversRaw

		mcpRaw, err := json.Marshal(mcpObj)
		if err != nil {
			return err
		}
		existing["mcp"] = mcpRaw
	}

	out, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return err
	}
	return iowriter.WriteAtomic(path, out, 0600)
}

func (b *JsonBackend) detectFlatMcpFormat(existing map[string]json.RawMessage) bool {
	mcpRaw, ok := existing["mcp"]
	if !ok {
		return false
	}
	var mcp map[string]json.RawMessage
	if err := json.Unmarshal(mcpRaw, &mcp); err != nil {
		return false
	}
	_, hasServers := mcp["servers"]
	_, hasServers2 := mcp["Servers"]
	return !hasServers && !hasServers2
}

func (b *JsonBackend) serverToOpencode(s Server) opencodeServer {
	oc := opencodeServer{}

	if s.Transport == TransportHTTP && s.URL != "" {
		oc.Type = "remote"
		oc.URL = s.URL
	} else {
		oc.Type = "local"
	}

	// OpenCode uses command as an array: [command, ...args]
	if s.Command != "" {
		cmd := []string{s.Command}
		cmd = append(cmd, s.Args...)
		oc.Command = cmd
	}

	if len(s.Env) > 0 {
		oc.Environment = s.Env
	}

	oc.Timeout = s.Timeout

	return oc
}
