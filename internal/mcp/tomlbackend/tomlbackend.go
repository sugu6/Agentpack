package tomlbackend

import (
	"agentpack/internal/config"
	"agentpack/internal/iowriter"
	"agentpack/internal/mcp/backend"
	"agentpack/internal/mcp/types"
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

func NewBackend() *TomlBackend { return &TomlBackend{} }

func (b *TomlBackend) BackupDir() string {
	return filepath.Join(config.AgentPackDir(), "backups", "mcp")
}

func (b *TomlBackend) Read(path string) (map[string]types.Server, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]types.Server{}, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return map[string]types.Server{}, nil
	}

	// Try Codex [[mcp_servers]] array format first
	// 条目以 Primitive 解码：一次解出显式建模字段，再解一遍原始键值保留
	// 未建模键（最新 Codex 的 env_vars / startup_timeout_sec / oauth 等），
	// 整表重写时经 Server.Extra 原样写回，不丢配置。
	var codexCfg struct {
		McpServers []toml.Primitive `toml:"mcp_servers"`
	}
	if err := toml.Unmarshal(data, &codexCfg); err == nil && len(codexCfg.McpServers) > 0 {
		out := make(map[string]types.Server, len(codexCfg.McpServers))
		partial := false
		var skipped []string
		for _, prim := range codexCfg.McpServers {
			var ts tomlMcpServer
			if derr := toml.PrimitiveDecode(prim, &ts); derr != nil {
				// 单条坏条目只跳过该条目并标记 partial（与 table 格式一致），
				// 不因一条坏条目导致整表重写时其余条目全部不可见
				log.Printf("mcp: skip unparseable entry in %s: %v", path, derr)
				partial = true
				skipped = append(skipped, "(unparseable)")
				continue
			}
			if ts.Name == "" {
				// 数组格式以 name 为条目 key，缺 name 的条目无法定位，跳过
				log.Printf("mcp: skip unnamed entry in %s", path)
				partial = true
				skipped = append(skipped, "(unnamed)")
				continue
			}
			if ts.Command == "" && ts.URL == "" {
				// 残缺条目（仅 name 无 command/url）：同样标记 partial，
				// 否则整表重写时该条目被静默删除
				log.Printf("mcp: skip incomplete entry %q in %s: missing command/url", ts.Name, path)
				partial = true
				skipped = append(skipped, ts.Name)
				continue
			}
			if ts.Command != "" {
				if err := types.ValidateCommand(ts.Command); err != nil {
					// 标记 partial：写路径必须拒绝，避免整表重写时静默删除该条目
					log.Printf("mcp: skip server %q in %s: %v", ts.Name, path, err)
					partial = true
					skipped = append(skipped, ts.Name)
					continue
				}
			}
			extra, xerr := extraFromPrimitive(prim)
			if xerr != nil {
				// 保留已建模字段，仅放弃额外键，不让单个键破坏整条条目
				log.Printf("mcp: keep entry %q in %s without extra keys: %v", ts.Name, path, xerr)
				extra = nil
			}
			transport := types.TransportStdio
			if ts.Type == "sse" {
				transport = types.TransportSSE
			} else if ts.Type == "http" {
				transport = types.TransportHTTP
			} else if ts.Type == "streamable-http" {
				transport = types.TransportStreamableHTTP
			} else if ts.Command == "" && ts.URL != "" {
				transport = types.TransportHTTP
			}
			args := ts.Args
			if len(args) == 0 {
				args = nil
			}
			s := types.Server{
				Name:       ts.Name,
				Command:    ts.Command,
				Args:       args,
				Env:        ts.Env,
				Transport:  transport,
				ConfigType: ts.Type,
				URL:        ts.URL,
				Timeout:    resolveTimeout(ts.Timeout, ts.ToolTimeoutSec),
				Cwd:        ts.Cwd,
				Headers:    mergeHeaders(ts.Headers, ts.HTTPHeaders),
				Enabled:    ts.Enabled,
				Extra:      extra,
				Source:     "config",
			}
			if s.ID == "" {
				s.ID = backend.ServerDeterministicID(ts.Name, path)
			}
			out[ts.Name] = s
		}
		if partial {
			// 错误信息带上被跳过的 server 名，方便用户定位配置中的坏条目
			return out, fmt.Errorf("%w; skipped entries in %s: %s", types.ErrPartialRead, path, strings.Join(skipped, ", "))
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
		return map[string]types.Server{}, nil
	}
	out := make(map[string]types.Server, len(raw.McpServers))
	partial := false
	var skipped []string
	for name, prim := range raw.McpServers {
		s, err := parseTomlServer(name, prim)
		if err != nil {
			// 单个坏条目只跳过该条目并标记 partial（与 JSON/数组格式一致），
			// 不因一个坏条目导致整个文件的服务器全部不可见
			log.Printf("mcp: skip server %q in %s: %v", name, path, err)
			partial = true
			skipped = append(skipped, name)
			continue
		}
		s.Source = "config"
		if s.ID == "" {
			s.ID = backend.ServerDeterministicID(name, path)
		}
		out[name] = s
	}
	if partial {
		return out, fmt.Errorf("%w; skipped entries in %s: %s", types.ErrPartialRead, path, strings.Join(skipped, ", "))
	}
	return out, nil
}

// tomlMcpServer represents a single [[mcp_servers]] entry in Codex config.toml
// type/headers/timeout 为旧版键（新版 Codex 不再识别，transport 由 command/url
// 有无隐式确定）；http_headers/tool_timeout_sec 为最新 Codex schema 对应键。
// 两套键都读、只写新键，保证新旧两代配置文件都能往返。
type tomlMcpServer struct {
	Name           string            `toml:"name"`
	Type           string            `toml:"type"`
	Command        string            `toml:"command"`
	Args           []string          `toml:"args"`
	Env            map[string]string `toml:"env"`
	Headers        map[string]string `toml:"headers"`      // 旧键
	HTTPHeaders    map[string]string `toml:"http_headers"` // 最新键
	URL            string            `toml:"url"`
	Timeout        int               `toml:"timeout"`          // 旧键
	ToolTimeoutSec float64           `toml:"tool_timeout_sec"` // 最新键（秒）
	Cwd            string            `toml:"cwd"`
	Enabled        *bool             `toml:"enabled"`
}

type tomlServer struct {
	Type           string            `toml:"type"`
	Command        string            `toml:"command"`
	Args           []string          `toml:"args"`
	Env            map[string]string `toml:"env"`
	Headers        map[string]string `toml:"headers"`      // 旧键
	HTTPHeaders    map[string]string `toml:"http_headers"` // 最新键
	URL            string            `toml:"url"`
	Timeout        int               `toml:"timeout"`          // 旧键
	ToolTimeoutSec float64           `toml:"tool_timeout_sec"` // 最新键（秒）
	Cwd            string            `toml:"cwd"`
	Enabled        *bool             `toml:"enabled"`
}

// codexKnownKeys 是本后端显式建模的条目键；其余键一律收进 Server.Extra 原样保留。
// 新增建模字段时必须同步维护此集合，否则该键会同时出现在 Extra 中被重复写出。
var codexKnownKeys = map[string]struct{}{
	"name": {}, "type": {}, "command": {}, "args": {}, "env": {},
	"headers": {}, "http_headers": {}, "url": {}, "timeout": {},
	"tool_timeout_sec": {}, "cwd": {}, "enabled": {},
}

// extraFromPrimitive 解出条目原始键值，返回未显式建模的部分（Extra 保留集）。
func extraFromPrimitive(prim toml.Primitive) (map[string]any, error) {
	var rawMap map[string]any
	if err := toml.PrimitiveDecode(prim, &rawMap); err != nil {
		return nil, err
	}
	extra := map[string]any{}
	for k, v := range rawMap {
		if _, known := codexKnownKeys[k]; !known {
			extra[k] = v
		}
	}
	if len(extra) == 0 {
		return nil, nil
	}
	return extra, nil
}

// mergeHeaders 合并旧 "headers" 键与最新 "http_headers" 键；冲突时 http_headers 优先。
func mergeHeaders(legacy, latest map[string]string) map[string]string {
	if len(legacy) == 0 && len(latest) == 0 {
		return nil
	}
	out := make(map[string]string, len(legacy)+len(latest))
	for k, v := range legacy {
		out[k] = v
	}
	for k, v := range latest {
		out[k] = v
	}
	return out
}

// resolveTimeout 优先取最新 tool_timeout_sec（秒），回退旧版 timeout。
func resolveTimeout(legacy int, latest float64) int {
	if latest > 0 {
		return int(latest)
	}
	return legacy
}

func parseTomlServer(name string, prim toml.Primitive) (types.Server, error) {
	var ts tomlServer
	if err := toml.PrimitiveDecode(prim, &ts); err != nil {
		return types.Server{}, err
	}
	transport := types.TransportStdio
	if ts.Type == "sse" {
		transport = types.TransportSSE
	} else if ts.Type == "http" {
		transport = types.TransportHTTP
	} else if ts.Type == "streamable-http" {
		transport = types.TransportStreamableHTTP
	} else if ts.Command == "" && ts.URL != "" {
		transport = types.TransportHTTP
	}
	if ts.Command == "" && ts.URL == "" {
		return types.Server{}, fmt.Errorf("server %q: command or url is required", name)
	}
	if transport == types.TransportStdio && ts.Command == "" {
		return types.Server{}, fmt.Errorf("server %q: command is required", name)
	}
	if ts.Command != "" {
		if err := types.ValidateCommand(ts.Command); err != nil {
			return types.Server{}, fmt.Errorf("server %q: %w", name, err)
		}
	}
	args := ts.Args
	if len(args) == 0 {
		args = nil
	}
	extra, err := extraFromPrimitive(prim)
	if err != nil {
		// 保留已建模字段，仅放弃额外键，不让单个键破坏整条条目
		extra = nil
	}
	return types.Server{
		Name:       name,
		Command:    ts.Command,
		Args:       args,
		Env:        ts.Env,
		Transport:  transport,
		ConfigType: ts.Type,
		URL:        ts.URL,
		Timeout:    resolveTimeout(ts.Timeout, ts.ToolTimeoutSec),
		Cwd:        ts.Cwd,
		Headers:    mergeHeaders(ts.Headers, ts.HTTPHeaders),
		Source:     "config",
		Enabled:    ts.Enabled,
		Extra:      extra,
	}, nil
}

func (b *TomlBackend) Write(path string, servers map[string]types.Server) error {
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

func (b *TomlBackend) writeArrayFormat(path string, existing map[string]any, servers map[string]types.Server) error {
	mcpServers := make([]map[string]any, 0, len(servers))
	for _, s := range servers {
		entry := map[string]any{"name": s.Name}
		if s.Command != "" {
			entry["command"] = s.Command
		}
		// 不写 type 键：新版 Codex 不认识该键（触发 "unrecognized
		// configuration settings" 警告），传输方式由 command/url 有无隐式确定。
		if len(s.Args) > 0 {
			entry["args"] = s.Args
		}
		if len(s.Env) > 0 {
			entry["env"] = s.Env
		}
		// 最新 Codex schema 的请求头键为 http_headers（旧 headers 键被忽略）
		if len(s.Headers) > 0 {
			entry["http_headers"] = s.Headers
		}
		if s.URL != "" {
			entry["url"] = s.URL
		}
		// 最新 Codex schema 的超时键为 tool_timeout_sec（秒）
		if s.Timeout > 0 {
			entry["tool_timeout_sec"] = s.Timeout
		}
		if s.Enabled != nil {
			entry["enabled"] = *s.Enabled
		}
		if s.Cwd != "" {
			entry["cwd"] = s.Cwd
		}
		// 原样写回未显式建模的键（如应用自管理的 env_vars /
		// startup_timeout_sec / oauth），跳过与显式键冲突者
		for k, v := range s.Extra {
			if writtenKey(k) {
				continue
			}
			entry[k] = v
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

func (b *TomlBackend) writeTableFormat(path string, existing map[string]any, servers map[string]types.Server) error {
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
		// 不写 type 键：新版 Codex 不认识该键（触发 "unrecognized
		// configuration settings" 警告），传输方式由 command/url 有无隐式确定。
		if s.Command != "" {
			fmt.Fprintf(&buf, "command = %s\n", tomlQuoteValue(s.Command))
		}
		if len(s.Args) > 0 {
			buf.WriteString("args = [")
			for i, a := range s.Args {
				if i > 0 {
					buf.WriteString(", ")
				}
				fmt.Fprintf(&buf, "%s", tomlQuoteValue(a))
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
		// 最新 Codex schema 的请求头键为 http_headers（旧 headers 键被忽略）
		if len(s.Headers) > 0 {
			buf.WriteString("http_headers = {")
			headerKeys := make([]string, 0, len(s.Headers))
			for k := range s.Headers {
				headerKeys = append(headerKeys, k)
			}
			sort.Strings(headerKeys)
			for i, k := range headerKeys {
				if i > 0 {
					buf.WriteString(", ")
				}
				fmt.Fprintf(&buf, "%s = %s", quoteTomlKey(k), tomlQuoteValue(s.Headers[k]))
			}
			buf.WriteString("}\n")
		}
		if s.URL != "" {
			fmt.Fprintf(&buf, "url = %s\n", tomlQuoteValue(s.URL))
		}
		// 最新 Codex schema 的超时键为 tool_timeout_sec（秒）
		if s.Timeout > 0 {
			fmt.Fprintf(&buf, "tool_timeout_sec = %d\n", s.Timeout)
		}
		if s.Enabled != nil {
			fmt.Fprintf(&buf, "enabled = %t\n", *s.Enabled)
		}
		if s.Cwd != "" {
			fmt.Fprintf(&buf, "cwd = %s\n", tomlQuoteValue(s.Cwd))
		}
		// 原样写回未显式建模的键（如应用自管理的 env_vars /
		// startup_timeout_sec / oauth），跳过与显式键冲突者
		extraKeys := make([]string, 0, len(s.Extra))
		for k := range s.Extra {
			if !writtenKey(k) {
				extraKeys = append(extraKeys, k)
			}
		}
		sort.Strings(extraKeys)
		for _, k := range extraKeys {
			fmt.Fprintf(&buf, "%s = ", quoteTomlKey(k))
			writeTomlValue(&buf, s.Extra[k])
			buf.WriteByte('\n')
		}
		buf.WriteByte('\n')
	}

	return iowriter.WriteAtomic(path, buf.Bytes(), 0600)
}

// writtenKey 判断键是否由写路径显式输出，防止 Extra 键与其重名导致
// 同一键出现两次（生成非法 TOML）。
func writtenKey(k string) bool {
	switch k {
	case "name", "command", "args", "env", "http_headers", "url", "tool_timeout_sec", "enabled", "cwd":
		return true
	}
	return false
}

// quoteTomlKey quotes anything outside TOML's bare-key character set.
// 引号 key 使用 TOML 兼容转义（与 tomlQuoteValue 一致，避免 %q 输出
// \xNN 产生非法 TOML——key 与值共用基本字符串转义规则）。
func quoteTomlKey(name string) string {
	if name == "" {
		return `""`
	}
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			continue
		}
		return tomlQuoteValue(name)
	}
	return name
}

func writeTomlValue(buf *bytes.Buffer, val any) {
	switch v := val.(type) {
	case string:
		buf.WriteString(tomlQuoteValue(v))
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
			fmt.Fprintf(buf, "%s = ", quoteTomlKey(k))
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
		buf.WriteString(tomlQuoteValue(string(b)))
	}
}

// tomlQuoteValue 返回 TOML 兼容的字符串值。
// 正确处理引号、换行、控制字符等特殊场景，避免生成非法 TOML。
func tomlQuoteValue(val string) string {
	// 如果字符串不含特殊字符，使用普通双引号基本字符串
	// TOML 基本字符串要求转义 0x00-0x1F 控制字符与 0x7F（DEL）
	needsEscape := false
	for _, c := range val {
		if c == '"' || c == '\\' || c == '\n' || c == '\r' || c == '\t' || c < 0x20 || c == 0x7f {
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
			if c < 0x20 || c == 0x7f {
				// 其他控制字符（含 DEL）使用 Unicode 转义
				fmt.Fprintf(&b, "\\u%04x", c)
			} else {
				b.WriteRune(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
