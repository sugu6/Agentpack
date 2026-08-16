package mcp

import (
	"agentpack/internal/config"
	"agentpack/internal/iowriter"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
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
			if err := json.Unmarshal(mcpRaw, &mcp); err != nil {
				// opencode 官方支持 "mcp": "off"/false/"" 禁用全部 MCP：
				// 这些值无法反序列化为 map 但语义明确（无任何服务器），
				// 按空集解析；若按整文件错误上报，Load 置 partialRead、
				// RescanAgents 的 Ready() 为 false，整个重扫功能失败，
				// 该 agent 的全部服务器不可见。
				if b.isOpencode() && isDisabledMcpValue(mcpRaw) {
					return map[string]Server{}, nil
				}
				// 容器字段异常（如 []、畸形 JSON）：与写路径 writeOpencode
				// 对畸形 mcp 字段明确报错拒绝覆盖的行为对齐。opencode 下静默
				// 吞掉会让该 agent 的全部服务器"消失"且 Load 无错误提示；
				// 按整文件错误上报，Load 侧置 partialRead 保留基线不删管理状态。
				if b.isOpencode() {
					return nil, fmt.Errorf("parse mcp field in %s: %w", path, err)
				}
				// 非 opencode 容器：结构未知，仅记录，不毒化整个文件
				log.Printf("mcp: skip unparsable mcp field in %s: %v", path, err)
			} else if srvRaw, ok3 := mcp["servers"]; ok3 {
				raw = srvRaw
			} else if b.isOpencode() {
				raw = mcpRaw
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
	partial := false
	var skipped []string
	for name, raw := range mcServers {
		s, err := b.parseJsonServer(name, raw)
		if err != nil {
			// 单条 entry 解析失败（危险命令/非法 JSON）只跳过该条，不毒化整个文件，
			// 与 TomlBackend 对残缺 [[mcp_servers]] 条目的容忍行为保持一致。
			// 同时标记 partial：写路径必须拒绝，避免整表重写时静默删除该条目。
			log.Printf("mcp: skip server %q in %s: %v", name, path, err)
			partial = true
			skipped = append(skipped, name)
			continue
		}
		s.Source = "config"
		if s.ID == "" {
			s.ID = serverDeterministicID(name, path)
		}
		out[name] = s
	}
	if partial {
		// 错误信息带上被跳过的 server 名，方便用户定位配置中的坏条目
		return out, fmt.Errorf("%w; skipped entries in %s: %s", ErrPartialRead, path, strings.Join(skipped, ", "))
	}
	return out, nil
}

// isDisabledMcpValue 判断 mcp 字段是否为 opencode 的"禁用全部 MCP"写法。
// 官方支持 "off"/false/""/true 等标量值（无法反序列化为 map 但语义明确），
// 读取时应解析为空集而不是整文件错误（否则 Rescan 整体失败）。
func isDisabledMcpValue(raw json.RawMessage) bool {
	switch strings.TrimSpace(string(raw)) {
	case "", `"off"`, `"false"`, `false`, `"true"`, `true`, `"disabled"`:
		return true
	}
	return false
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
	Headers    map[string]string `json:"headers,omitempty"`
}

// opencodeServer is the OpenCode-specific JSON format where command is an array
type opencodeServer struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command"`
	Environment map[string]string `json:"environment"`
	// Env 兼容用户按 Claude Code 习惯手写的 "env" 键：opencode 官方用
	// "environment"，但手写配置里 "env" 很常见。标准 jsonServer 有 Env 标签，
	// 若 opencodeServer 不加此字段，json.Unmarshal 会静默忽略未知键，
	// env 直接丢失且后续写回也不复存在。
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Enabled *bool             `json:"enabled,omitempty"`
	Timeout int               `json:"timeout"`
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
	// 残缺条目（无命令且无 URL）没有可执行内容：静默接受会生成无法管理的
	// 幽灵条目（Add 时被 ValidateCommand 拒绝），且写路径整表重写会"保留"
	// 坏条目。与 TOML 后端行为对齐：拒绝并标记 partial。
	if js.Command == "" && js.URL == "" {
		return Server{}, fmt.Errorf("server %q: missing command and url", name)
	}
	transport := TransportStdio
	if js.Transport == "sse" || js.Type == "sse" {
		// Claude Code / Cursor 的远程服务器格式为 {"type":"sse","url":...}
		transport = TransportSSE
	} else if js.Transport == "http" || js.Type == "http" {
		transport = TransportHTTP
	} else if js.Transport == "streamable-http" || js.Type == "streamable-http" {
		transport = TransportStreamableHTTP
	} else if js.Command == "" && js.URL != "" {
		// 手写/精简配置只给 url 不带 type：按远程传输处理（与 TOML 后端一致）。
		// 否则解析为 stdio+空 Command，Add/Update 会被 ValidateCommand 拒绝
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
		Headers:    js.Headers,
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
	switch oc.Type {
	case "remote", "http":
		transport = TransportHTTP
	case "sse":
		// opencode 的 "sse" 使用 SSE 协议客户端，与 "remote"（Streamable HTTP）
		// 协议不兼容，必须保留区分度，否则写回时 type 被改写成 "remote"，
		// 重启后连接失败。
		transport = TransportSSE
	case "streamable-http", "streamableHttp":
		transport = TransportStreamableHTTP
	default:
		// "local"、空或未知 type：有 URL 且无命令时按远程传输处理，
		// 否则 stdio+URL 畸形条目写回时 URL 会丢失（serverToOpencode
		// 只对远程传输写 url，stdio 分支输出空 url，远程配置被永久破坏）。
		if oc.URL != "" && len(oc.Command) == 0 {
			transport = TransportHTTP
		}
	}
	// 残缺条目（无命令且无 URL）没有可执行内容：拒绝并标记 partial，
	// 与标准 JSON/TOML 后端行为一致。
	if command == "" && oc.URL == "" {
		return Server{}, fmt.Errorf("server %q: missing command and url", name)
	}

	// 归一化：空切片视为 nil
	if len(args) == 0 {
		args = nil
	}
	env := oc.Environment
	if env == nil {
		// "environment" 缺失时退回 "env"（用户手写的兼容键）
		env = oc.Env
	}

	return Server{
		Name:       name,
		Command:    command,
		Args:       args,
		Env:        env,
		Transport:  transport,
		ConfigType: oc.Type,
		URL:        oc.URL,
		Timeout:    oc.Timeout,
		Headers:    oc.Headers,
		Enabled:    oc.Enabled,
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
			Headers:    s.Headers,
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

	// Preserve the container key detected by Read. Some agents use the
	// workspace-compatible top-level "servers" key instead of "mcpServers".
	mcServersRaw, err := json.Marshal(mcServers)
	if err != nil {
		return err
	}
	_, hasMcpServers := existing["mcpServers"]
	_, hasServers := existing["servers"]
	containerKeys := []string{"mcpServers"}
	switch {
	case hasMcpServers && hasServers:
		// 异常双容器配置：同步更新两个容器，避免删除/更新后另一容器残留旧条目
		containerKeys = []string{"mcpServers", "servers"}
	case hasServers:
		containerKeys = []string{"servers"}
	}
	for _, k := range containerKeys {
		existing[k] = mcServersRaw
	}

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
			if err := json.Unmarshal(mcpRaw, &mcpObj); err != nil {
				return fmt.Errorf("parse existing mcp field: %w", err)
			}
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

	// 只要 URL 非空就写回：含 URL 的条目（无论传输标记为何）必须保留 url 字段，
	// 否则 stdio+URL 等畸形解析结果会让远程服务器配置在写回时永久丢失 URL。
	if s.URL != "" && (s.Transport == TransportHTTP || s.Transport == TransportSSE || s.Transport == TransportStreamableHTTP || s.Command == "") {
		// 优先还原原始 type：opencode 的 "sse"/"streamable-http" 与 "remote"
		// 使用不同的客户端协议（sse 用 SSE 传输、remote 用 Streamable HTTP），
		// 统一写 "remote" 会静默改变连接协议，重启后连接失败。仅当原始 type
		// 缺失或无法识别时才用 Transport 推导。
		switch s.ConfigType {
		case "remote", "sse", "http", "streamable-http", "streamableHttp":
			oc.Type = s.ConfigType
		default:
			oc.Type = "remote"
		}
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

	if len(s.Headers) > 0 {
		oc.Headers = s.Headers
	}

	// opencode 条目省略 enabled 时默认为 true；只有显式禁用（false）才写出，
	// 与 opencode 自身的省略语义一致，同时保留用户手动禁用的状态。
	if s.Enabled != nil && !*s.Enabled {
		oc.Enabled = s.Enabled
	}

	return oc
}
