package jsonbackend

import (
	"agentpack/internal/config"
	"agentpack/internal/iowriter"
	"agentpack/internal/mcp/backend"
	"agentpack/internal/mcp/types"
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

func NewBackend(agentType string) *JsonBackend {
	return &JsonBackend{agentType: agentType}
}

func (b *JsonBackend) BackupDir() string {
	return filepath.Join(config.AgentPackDir(), "backups", "mcp")
}

func (b *JsonBackend) isOpencode() bool {
	return b.agentType == "opencode"
}

func (b *JsonBackend) Read(path string) (map[string]types.Server, error) {
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
					return map[string]types.Server{}, nil
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
		return map[string]types.Server{}, nil
	}

	var mcServers map[string]json.RawMessage
	if err := json.Unmarshal(raw, &mcServers); err != nil {
		return nil, fmt.Errorf("parse mcpServers in %s: %w", path, err)
	}

	out := make(map[string]types.Server, len(mcServers))
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
			s.ID = backend.ServerDeterministicID(name, path)
		}
		out[name] = s
	}
	if partial {
		// 错误信息带上被跳过的 server 名，方便用户定位配置中的坏条目
		return out, fmt.Errorf("%w; skipped entries in %s: %s", types.ErrPartialRead, path, strings.Join(skipped, ", "))
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
	Environment map[string]string `json:"environment,omitempty"`
	// Env 兼容用户按 Claude Code 习惯手写的 "env" 键：opencode 官方用
	// "environment"，但手写配置里 "env" 很常见。标准 jsonServer 有 Env 标签，
	// 若 opencodeServer 不加此字段，json.Unmarshal 会静默忽略未知键，
	// env 直接丢失且后续写回也不复存在。
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Enabled *bool             `json:"enabled,omitempty"`
	// Cwd 对应 opencode 最新 schema LocalServer.cwd（工作目录，相对路径
	// 以工作区为基准解析）；旧版 opencode 无此键，缺失时读写均不受影响。
	Cwd     string `json:"cwd,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
}

// jsonServerKnownKeys 是标准 JSON 后端（Claude Code / Cursor / Trae）显式建模的
// 条目键；其余键一律收进 Server.Extra 原样保留。
// 最新各 agent 的 agent 专属字段（Claude 的 oauth/headersHelper/alwaysLoad、
// Cursor 的 envFile/auth、Trae 的 disabled 等）都走 Extra，整表重写不丢配置。
var jsonServerKnownKeys = map[string]struct{}{
	"command": {}, "args": {}, "env": {}, "transport": {}, "url": {},
	"timeout": {}, "cwd": {}, "type": {}, "headers": {},
}

// opencodeServerKnownKeys 是 OpenCode 后端显式建模的条目键（对齐最新
// opencode schema 的 Local/Remote server 字段）。
// 官方 schema 对条目声明 additionalProperties:false（Effect 严格解码），
// 因此 opencode 的 Extra 只允许回写官方支持但未显式建模的键（见
// opencodeExtraWriteKeys），未知键写出会破坏该 agent 的配置校验。
var opencodeServerKnownKeys = map[string]struct{}{
	"type": {}, "command": {}, "environment": {}, "env": {}, "url": {},
	"headers": {}, "enabled": {}, "timeout": {}, "cwd": {},
}

// opencodeExtraWriteKeys：官方 schema 支持、但本工具未显式建模的条目键。
// 写回时只放行这些键（严格 schema 下的无损保留）。
var opencodeExtraWriteKeys = map[string]struct{}{
	"oauth": {},
}

// jsonExtraFor 解出条目原始键值，返回未显式建模的部分（Extra 保留集）。
func jsonExtraFor(raw json.RawMessage, known map[string]struct{}) (map[string]any, error) {
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	extra := map[string]any{}
	for k, v := range fields {
		if _, ok := known[k]; !ok {
			extra[k] = v
		}
	}
	if len(extra) == 0 {
		return nil, nil
	}
	return extra, nil
}

// mergeJSONExtras 把 Extra 键并入写出的条目，跳过与显式键冲突者
// （理论上 Extra 与 known 键不相交，此检查仅作防御）。
func mergeJSONExtras(entry map[string]any, extra map[string]any) {
	for k, v := range extra {
		if _, exists := entry[k]; !exists {
			entry[k] = v
		}
	}
}

// whitelistJSONExtras 与 mergeJSONExtras 相同，但只放行 allow 白名单内的键。
// 用于配置 schema 严格校验（additionalProperties:false）的 agent：未知键
// 写出会破坏该 agent 的配置合法性，宁可丢弃也不回写。
func whitelistJSONExtras(entry map[string]any, extra map[string]any, allow map[string]struct{}) {
	for k, v := range extra {
		if _, ok := allow[k]; !ok {
			continue
		}
		if _, exists := entry[k]; !exists {
			entry[k] = v
		}
	}
}

func (b *JsonBackend) parseJsonServer(name string, raw json.RawMessage) (types.Server, error) {
	if b.isOpencode() {
		return b.parseOpencodeServer(name, raw)
	}
	return b.parseStandardJsonServer(name, raw)
}

func (b *JsonBackend) parseStandardJsonServer(name string, raw json.RawMessage) (types.Server, error) {
	var js jsonServer
	if err := json.Unmarshal(raw, &js); err != nil {
		return types.Server{}, err
	}
	// 验证命令不包含危险的 shell 元字符（与TomlBackend 和Store.Add/Update 保持一致）
	if js.Command != "" {
		if err := types.ValidateCommand(js.Command); err != nil {
			return types.Server{}, fmt.Errorf("server %q: %w", name, err)
		}
	}
	// 残缺条目（无命令且无 URL）没有可执行内容：静默接受会生成无法管理的
	// 幽灵条目（Add 时被 types.ValidateCommand 拒绝），且写路径整表重写会"保留"
	// 坏条目。与 TOML 后端行为对齐：拒绝并标记 partial。
	if js.Command == "" && js.URL == "" {
		return types.Server{}, fmt.Errorf("server %q: missing command and url", name)
	}
	transport := types.TransportStdio
	if js.Transport == "sse" || js.Type == "sse" {
		// Claude Code / Cursor 的远程服务器格式为 {"type":"sse","url":...}
		transport = types.TransportSSE
	} else if js.Transport == "http" || js.Type == "http" {
		transport = types.TransportHTTP
	} else if js.Transport == "streamable-http" || js.Type == "streamable-http" {
		transport = types.TransportStreamableHTTP
	} else if js.Command == "" && js.URL != "" {
		// 手写/精简配置只给 url 不带 type：按远程传输处理（与 TOML 后端一致）。
		// 否则解析为 stdio+空 Command，Add/Update 会被 types.ValidateCommand 拒绝
		transport = types.TransportHTTP
	}
	// 归一化：空切片视为 nil，保证读写一致性
	args := js.Args
	if len(args) == 0 {
		args = nil
	}
	// 未建模键（Claude 的 oauth/headersHelper/alwaysLoad、Cursor 的 envFile/auth、
	// Trae 的 disabled 等）收进 Extra，整表重写时原样写回，不丢配置
	extra, err := jsonExtraFor(raw, jsonServerKnownKeys)
	if err != nil {
		// 保留已建模字段，仅放弃额外键，不让单个键破坏整条条目
		extra = nil
	}
	return types.Server{
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
		Extra:      extra,
	}, nil
}

func (b *JsonBackend) parseOpencodeServer(name string, raw json.RawMessage) (types.Server, error) {
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
		if err := types.ValidateCommand(command); err != nil {
			return types.Server{}, fmt.Errorf("server %q: %w", name, err)
		}
	}

	transport := types.TransportStdio
	switch oc.Type {
	case "remote", "http":
		transport = types.TransportHTTP
	case "sse":
		// opencode 的 "sse" 使用 SSE 协议客户端，与 "remote"（Streamable HTTP）
		// 协议不兼容，必须保留区分度，否则写回时 type 被改写成 "remote"，
		// 重启后连接失败。
		transport = types.TransportSSE
	case "streamable-http", "streamableHttp":
		transport = types.TransportStreamableHTTP
	default:
		// "local"、空或未知 type：有 URL 且无命令时按远程传输处理，
		// 否则 stdio+URL 畸形条目写回时 URL 会丢失（serverToOpencode
		// 只对远程传输写 url，stdio 分支输出空 url，远程配置被永久破坏）。
		if oc.URL != "" && len(oc.Command) == 0 {
			transport = types.TransportHTTP
		}
	}
	// 残缺条目（无命令且无 URL）没有可执行内容：拒绝并标记 partial，
	// 与标准 JSON/TOML 后端行为一致。
	if command == "" && oc.URL == "" {
		// 官方 schema 允许最小覆盖条目 {"enabled": bool}（启用/禁用组织远端
		// .well-known/opencode 提供的 MCP server，本身无 command/url）：作为
		// 合法条目读入。若按坏条目跳过并标记 partial，Rescan 会整体失败
		// （ErrPartialRead 级联），且整表重写会把这些条目从配置中删除。
		if oc.Enabled != nil {
			return types.Server{
				Name:    name,
				Enabled: oc.Enabled,
				Source:  "config",
			}, nil
		}
		return types.Server{}, fmt.Errorf("server %q: missing command and url", name)
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
	// 未建模键（最新 opencode schema 的 remote oauth 等）收进 Extra，
	// 整表重写时原样写回，不丢配置
	extra, err := jsonExtraFor(raw, opencodeServerKnownKeys)
	if err != nil {
		extra = nil
	}
	return types.Server{
		Name:       name,
		Command:    command,
		Args:       args,
		Env:        env,
		Transport:  transport,
		ConfigType: oc.Type,
		URL:        oc.URL,
		Timeout:    oc.Timeout,
		Cwd:        oc.Cwd,
		Headers:    oc.Headers,
		Enabled:    oc.Enabled,
		Source:     "config",
		Extra:      extra,
	}, nil
}

func (b *JsonBackend) Write(path string, servers map[string]types.Server) error {
	if b.isOpencode() {
		return b.writeOpencode(path, servers)
	}
	return b.writeStandard(path, servers)
}

func (b *JsonBackend) writeStandard(path string, servers map[string]types.Server) error {
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
		// 按"仅写出已设置字段"构造条目：避免 "command": "" / "args": null
		// 这类空值噪声进入各 agent 的配置文件。
		entry := map[string]any{}
		if s.Command != "" {
			entry["command"] = s.Command
		}
		// 各 agent（Claude Code / Cursor / Trae）最新配置均使用 type 字段；
		// 原始配置没有时根据 transport 推导。两者都未设置时不写该键——
		// 写出 "type": "" 是未设置字段的噪声（CHANGELOG 承诺不再写出）。
		if s.ConfigType != "" {
			entry["type"] = s.ConfigType
		} else if t := string(s.Transport); t != "" {
			entry["type"] = t
		}
		if len(s.Args) > 0 {
			entry["args"] = s.Args
		}
		if len(s.Env) > 0 {
			entry["env"] = s.Env
		}
		if len(s.Headers) > 0 {
			entry["headers"] = s.Headers
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
		// 原样写回未显式建模的键（oauth / envFile / disabled 等）
		mergeJSONExtras(entry, s.Extra)
		encoded, err := json.Marshal(entry)
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

func (b *JsonBackend) writeOpencode(path string, servers map[string]types.Server) error {
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
		entry := b.opencodeEntry(s)
		encoded, err := json.Marshal(entry)
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

// opencodeEntry 构造单个 OpenCode 条目的写出键值（仅含已设置字段），并并入
// Extra 保留的官方未建模键（如最新 schema 的 remote oauth 配置）。
func (b *JsonBackend) opencodeEntry(s types.Server) map[string]any {
	// 最小覆盖条目（仅 enabled，无 command/url）：官方 schema 的该对象
	// 只允许 enabled 键，原样写回。
	if s.Command == "" && s.URL == "" && s.Enabled != nil {
		return map[string]any{"enabled": *s.Enabled}
	}

	entry := map[string]any{}

	// 只要 URL 非空就写回：含 URL 的条目（无论传输标记为何）必须保留 url 字段，
	// 否则 stdio+URL 等畸形解析结果会让远程服务器配置在写回时永久丢失 URL。
	if s.URL != "" && (s.Transport == types.TransportHTTP || s.Transport == types.TransportSSE || s.Transport == types.TransportStreamableHTTP || s.Command == "") {
		// 优先还原原始 type：opencode 的 "sse"/"streamable-http" 与 "remote"
		// 使用不同的客户端协议（sse 用 SSE 传输、remote 用 Streamable HTTP），
		// 统一写 "remote" 会静默改变连接协议，重启后连接失败。仅当原始 type
		// 缺失或无法识别时才用 types.Transport 推导。
		switch s.ConfigType {
		case "remote", "sse", "http", "streamable-http", "streamableHttp":
			entry["type"] = s.ConfigType
		default:
			entry["type"] = "remote"
		}
		entry["url"] = s.URL
	} else {
		entry["type"] = "local"
	}

	// OpenCode uses command as an array: [command, ...args]
	if s.Command != "" {
		cmd := []string{s.Command}
		cmd = append(cmd, s.Args...)
		entry["command"] = cmd
	}

	// 官方键为 "environment"；旧文件手写的 "env" 在读取时已归一化，
	// 写回统一用 "environment"（最新 schema 只认 environment）
	if len(s.Env) > 0 {
		entry["environment"] = s.Env
	}

	if s.Timeout > 0 {
		entry["timeout"] = s.Timeout
	}

	if len(s.Headers) > 0 {
		entry["headers"] = s.Headers
	}

	// 最新 opencode schema 的 local server 支持 cwd 工作目录
	if entry["type"] == "local" && s.Cwd != "" {
		entry["cwd"] = s.Cwd
	}

	// opencode 条目省略 enabled 时默认为 true；只有显式禁用（false）才写出，
	// 与 opencode 自身的省略语义一致，同时保留用户手动禁用的状态。
	if s.Enabled != nil && !*s.Enabled {
		entry["enabled"] = false
	}

	// 原样写回未显式建模的键（如 remote 的 oauth 配置）
	whitelistJSONExtras(entry, s.Extra, opencodeExtraWriteKeys)

	return entry
}
