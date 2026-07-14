package mcp

import (
	"agentpack/internal/config"
	"agentpack/internal/iowriter"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

type TomlBackend struct {
	mu sync.Mutex
}

func NewTomlBackend() *TomlBackend { return &TomlBackend{} }

func (b *TomlBackend) BackupDir() string {
	return filepath.Join(config.AgentPackDir(), "backups", "mcp")
}

func (b *TomlBackend) Read(path string) (map[string]Server, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
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

	// Try Codex [[mcp_servers]] array format first
	var codexCfg struct {
		McpServers []tomlMcpServer `toml:"mcp_servers"`
	}
	if err := toml.Unmarshal(data, &codexCfg); err == nil && len(codexCfg.McpServers) > 0 {
		out := make(map[string]Server, len(codexCfg.McpServers))
		for _, ts := range codexCfg.McpServers {
			if ts.Command == "" && ts.URL == "" {
				continue // skip invalid entries
			}
			if ts.Command != "" {
				if err := ValidateCommand(ts.Command); err != nil {
					return nil, fmt.Errorf("server %q: %w", ts.Name, err)
				}
			}
			transport := TransportStdio
			if ts.Type == "sse" {
				transport = TransportSSE
			} else if ts.Type == "http" {
				transport = TransportHTTP
			} else if ts.Command == "" && ts.URL != "" {
				transport = TransportHTTP
			}
			args := ts.Args
			if len(args) == 0 {
				args = nil
			}
			s := Server{
				Name:       ts.Name,
				Command:    ts.Command,
				Args:       args,
				Env:        ts.Env,
				Transport:  transport,
				ConfigType: ts.Type,
				URL:        ts.URL,
				Timeout:    ts.Timeout,
				Cwd:        ts.Cwd,
				Source:     "config",
			}
			if s.ID == "" {
				s.ID = ts.Name + "@" + path
			}
			out[ts.Name] = s
		}
		return out, nil
	}

	// Fallback: try [mcp_servers.NAME] table format
	var raw struct {
		McpServers map[string]toml.Primitive `toml:"mcp_servers"`
	}
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if raw.McpServers == nil {
		return map[string]Server{}, nil
	}
	out := make(map[string]Server, len(raw.McpServers))
	for name, prim := range raw.McpServers {
		s, err := parseTomlServer(name, prim)
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

// tomlMcpServer represents a single [[mcp_servers]] entry in Codex config.toml
type tomlMcpServer struct {
	Name    string            `toml:"name"`
	Type    string            `toml:"type"`
	Command string            `toml:"command"`
	Args    []string          `toml:"args"`
	Env     map[string]string `toml:"env"`
	URL     string            `toml:"url"`
	Timeout int               `toml:"timeout"`
	Cwd     string            `toml:"cwd"`
}

type tomlServer struct {
	Type    string            `toml:"type"`
	Command string            `toml:"command"`
	Args    []string          `toml:"args"`
	Env     map[string]string `toml:"env"`
	URL     string            `toml:"url"`
	Timeout int               `toml:"timeout"`
	Cwd     string            `toml:"cwd"`
}

func parseTomlServer(name string, prim toml.Primitive) (Server, error) {
	var ts tomlServer
	if err := toml.PrimitiveDecode(prim, &ts); err != nil {
		return Server{}, err
	}
	transport := TransportStdio
	if ts.Type == "sse" {
		transport = TransportSSE
	} else if ts.Type == "http" {
		transport = TransportHTTP
	} else if ts.Command == "" && ts.URL != "" {
		transport = TransportHTTP
	}
	if ts.Command == "" && ts.URL == "" {
		return Server{}, fmt.Errorf("server %q: command or url is required", name)
	}
	if transport == TransportStdio && ts.Command == "" {
		return Server{}, fmt.Errorf("server %q: command is required", name)
	}
	if ts.Command != "" {
		if err := ValidateCommand(ts.Command); err != nil {
			return Server{}, fmt.Errorf("server %q: %w", name, err)
		}
	}
	args := ts.Args
	if len(args) == 0 {
		args = nil
	}
	return Server{
		Name:       name,
		Command:    ts.Command,
		Args:       args,
		Env:        ts.Env,
		Transport:  transport,
		ConfigType: ts.Type,
		URL:        ts.URL,
		Timeout:    ts.Timeout,
		Cwd:        ts.Cwd,
		Source:     "config",
	}, nil
}

func (b *TomlBackend) Write(path string, servers map[string]Server) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	existing := make(map[string]any)
	data, err := os.ReadFile(path)
	if err == nil && len(data) > 0 {
		if uerr := toml.Unmarshal(data, &existing); uerr != nil {
			return fmt.Errorf("refuse to overwrite %s: existing file is not valid TOML: %w", path, uerr)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read existing %s: %w", path, err)
	}

	// 每次写入时从现有文件内容重新检测格式，避免新实例丢失格式信息
	if b.detectTableFormat(data) {
		return b.writeTableFormat(path, existing, servers)
	}
	return b.writeArrayFormat(path, existing, servers)
}

// detectTableFormat 检查已有文件数据是否使用[mcp_servers.NAME] table 格式
func (b *TomlBackend) detectTableFormat(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	var raw struct {
		McpServers map[string]toml.Primitive `toml:"mcp_servers"`
	}
	if err := toml.Unmarshal(data, &raw); err != nil {
		return false
	}
	return len(raw.McpServers) > 0
}

func (b *TomlBackend) writeArrayFormat(path string, existing map[string]any, servers map[string]Server) error {
	mcpServers := make([]map[string]any, 0, len(servers))
	for _, s := range servers {
		entry := map[string]any{"name": s.Name}
		if s.Command != "" {
			entry["command"] = s.Command
		}
		if s.ConfigType != "" {
			entry["type"] = s.ConfigType
		} else {
			entry["type"] = string(s.Transport)
		}
		if len(s.Args) > 0 {
			entry["args"] = s.Args
		}
		if len(s.Env) > 0 {
			entry["env"] = s.Env
		}
		if s.URL != "" {
			entry["url"] = s.URL
		}
		if s.Timeout > 0 {
			entry["timeout"] = s.Timeout
		}
		if s.Cwd != "" {
			entry["cwd"] = s.Cwd
		}
		mcpServers = append(mcpServers, entry)
	}
	existing["mcp_servers"] = mcpServers

	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	if err := enc.Encode(existing); err != nil {
		return err
	}
	return iowriter.WriteAtomic(path, buf.Bytes(), 0600)
}

func (b *TomlBackend) writeTableFormat(path string, existing map[string]any, servers map[string]Server) error {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)

	var buf bytes.Buffer

	// 保留 mcp_servers 之外的所有字段（如model）
	existingKeys := make([]string, 0, len(existing))
	for key := range existing {
		if key == "mcp_servers" {
			continue
		}
		existingKeys = append(existingKeys, key)
	}
	sort.Strings(existingKeys)
	for _, key := range existingKeys {
		fmt.Fprintf(&buf, "%s = ", quoteTomlKey(key))
		writeTomlValue(&buf, existing[key])
		buf.WriteByte('\n')
	}

	buf.WriteString("\n[mcp_servers]\n\n")
	for _, name := range names {
		s := servers[name]
		fmt.Fprintf(&buf, "[mcp_servers.%s]\n", quoteTomlKey(name))
		if s.ConfigType != "" {
			fmt.Fprintf(&buf, "type = %q\n", s.ConfigType)
		} else {
			fmt.Fprintf(&buf, "type = %q\n", string(s.Transport))
		}
		if s.Command != "" {
			fmt.Fprintf(&buf, "command = %q\n", s.Command)
		}
		if len(s.Args) > 0 {
			buf.WriteString("args = [")
			for i, a := range s.Args {
				if i > 0 {
					buf.WriteString(", ")
				}
				fmt.Fprintf(&buf, "%q", a)
			}
			buf.WriteString("]\n")
		}
		if len(s.Env) > 0 {
			buf.WriteString("env = {")
			envKeys := make([]string, 0, len(s.Env))
			for k := range s.Env {
				envKeys = append(envKeys, k)
			}
			sort.Strings(envKeys)
			for i, k := range envKeys {
				if i > 0 {
					buf.WriteString(", ")
				}
				// 使用 TOML 兼容的转义，支持引号、换行等特殊字符
				fmt.Fprintf(&buf, "%s = %s", quoteTomlKey(k), tomlQuoteValue(s.Env[k]))
			}
			buf.WriteString("}\n")
		}
		if s.URL != "" {
			fmt.Fprintf(&buf, "url = %q\n", s.URL)
		}
		if s.Timeout > 0 {
			fmt.Fprintf(&buf, "timeout = %d\n", s.Timeout)
		}
		if s.Cwd != "" {
			fmt.Fprintf(&buf, "cwd = %q\n", s.Cwd)
		}
		buf.WriteByte('\n')
	}

	return iowriter.WriteAtomic(path, buf.Bytes(), 0600)
}

// quoteTomlKey quotes anything outside TOML's bare-key character set.
func quoteTomlKey(name string) string {
	if name == "" {
		return `""`
	}
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			continue
		}
		return fmt.Sprintf("%q", name)
	}
	return name
}

func writeTomlValue(buf *bytes.Buffer, val any) {
	switch v := val.(type) {
	case string:
		fmt.Fprintf(buf, "%q", v)
	case bool:
		fmt.Fprintf(buf, "%t", v)
	case int64:
		fmt.Fprintf(buf, "%d", v)
	case float64:
		fmt.Fprintf(buf, "%g", v)
	case []any:
		buf.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				buf.WriteString(", ")
			}
			writeTomlValue(buf, item)
		}
		buf.WriteByte(']')
	case map[string]any:
		buf.WriteByte('{')
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i, k := range keys {
			if i > 0 {
				buf.WriteString(", ")
			}
			fmt.Fprintf(buf, "%q = ", k)
			writeTomlValue(buf, v[k])
		}
		buf.WriteByte('}')
	default:
		// 未知类型无法直接映射为合法 TOML，尝试 JSON 序列化作为字符串值兜底，
		// 避免产生无效 TOML 导致整个配置文件不可解析
		b, err := json.Marshal(v)
		if err != nil {
			log.Printf("mcp: cannot serialize value of type %T to TOML, writing empty string", v)
			buf.WriteString(`""`)
			return
		}
		fmt.Fprintf(buf, "%q", string(b))
	}
}

// tomlQuoteValue 返回 TOML 兼容的字符串值。
// 正确处理引号、换行、控制字符等特殊场景，避免生成非法 TOML。
func tomlQuoteValue(val string) string {
	// 如果字符串不含特殊字符，使用普通双引号基本字符串
	needsEscape := false
	for _, c := range val {
		if c == '"' || c == '\\' || c == '\n' || c == '\r' || c == '\t' || (c >= 0 && c < 0x20) {
			needsEscape = true
			break
		}
	}
	if !needsEscape {
		return fmt.Sprintf("%q", val)
	}

	// 使用 TOML 转义序列
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range val {
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 {
				// 其他控制字符使用 Unicode 转义
				fmt.Fprintf(&b, "\\u%04x", c)
			} else {
				b.WriteRune(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
