package mcp

import (
	"agentpack/internal/agents"
	"agentpack/internal/crypto"
	"agentpack/internal/database"
	"agentpack/internal/dbutil"
	"agentpack/internal/iowriter"
	"agentpack/internal/shared"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type MutationHandler interface {
	OnMutation(action string, detail MutationDetail)
}

type MutationFunc func(action string, detail MutationDetail)

func (f MutationFunc) OnMutation(action string, detail MutationDetail) { f(action, detail) }

type MutationDetail struct {
	ServerID   string
	ServerName string
	Agents     []string
	OldServer  *Server
	OldConfigs map[string]string
}

// ErrDuplicateServer 表示 Add/Update 时发现归一化 key（命令/参数或 URL）相同的服务器已存在。
// 调用方可据此决定跳过而非中止整个流程（如备份导入）。
// 注意：错误文本带稳定前缀 "duplicate server:"，前端据此区分"同 key 已安装（可跳过）"
// 与"同名但 key 不同（真实冲突）"，修改文本时务必保留该前缀。
var ErrDuplicateServer = errors.New("duplicate server: server with same command/url already exists")

// ErrPartialRead 表示配置文件中部分条目无法解析（如危险命令），返回的条目集合可用但不完整。
// 只读路径（Load/Scan）应保留已解析条目；写路径（write/remove）必须拒绝，
// 否则整表重写会把未解析的条目静默删除。
var ErrPartialRead = errors.New("config contains unparsable entries that would be lost on rewrite")

type Store struct {
	mu       sync.RWMutex
	servers  map[string]Server
	bindings map[string]map[string]bool
	loaded   bool
	hook     MutationHandler
	// configCounts 记录最近一次 Load 中每个 agent 配置文件实际检测到的
	// 服务器数（按归一化 key 去重，未纳入管理的条目也计入），供 Agent
	// 页面展示"该 agent 配置里有多少 MCP 服务器"。
	configCounts map[string]int
}

func NewStore() *Store {
	return &Store{
		servers:      make(map[string]Server),
		bindings:     make(map[string]map[string]bool),
		configCounts: make(map[string]int),
	}
}

// Ready reports whether the last Load completed its state/database commit.
// A true value may still be accompanied by a partial-load error for one or
// more malformed agent configs.
func (s *Store) Ready() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loaded
}

func (s *Store) SetMutationHandler(h MutationHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hook = h
}

// notify 同步调用 hook 的 OnMutation 方法。若 hook 实现需要异步执行，
// 应在 hook 实现内部启动 goroutine 并加超时保护，避免阻塞 Store 调用方。
// 添加 recover 保护以防 Hook 实现中出现 panic。
func (s *Store) notify(action string, detail MutationDetail) {
	hook := func() MutationHandler {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.hook
	}()
	if hook == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("mutation hook panic: %v", r)
		}
	}()
	hook.OnMutation(action, detail)
}

func (s *Store) Load(reg *agents.Registry) error {
	s.mu.Lock()
	s.loaded = false
	s.mu.Unlock()

	// 上一会话同步进数据库的服务器 = 用户（或本应用）显式纳管的基线。
	// 配置文件里存在但基线中不存在的服务器保持"未管理"状态：
	// 不会自动进入列表（扫描后需用户显式加入），避免其他 agent 的
	// MCP 配置在应用重启后被静默纳入管理。
	baseline, err := s.loadManagedBaseline()
	if err != nil {
		return fmt.Errorf("load managed baseline: %w", err)
	}

	servers := make(map[string]Server)
	bindings := make(map[string]map[string]bool)
	configCounts := make(map[string]int)

	// 按ConfigPath 分组 agent，共享路径的 agent 只读一次文件
	pathGroups := make(map[string][]string) // configPath -> []agentID
	for _, ag := range reg.All() {
		if ag.ConfigPath == "" || ag.Status == agents.StatusNotFound {
			continue
		}
		pathGroups[ag.ConfigPath] = append(pathGroups[ag.ConfigPath], ag.ID)
	}

	var loadErrs []string
	for configPath, agentIDs := range pathGroups {
		// 用第一一agent 确定 backend 类型
		firstAgent := reg.Get(agentIDs[0])
		if firstAgent == nil {
			log.Printf("agent %s disappeared during load, skipping %s", agentIDs[0], configPath)
			continue
		}
		backend := NewBackend(string(firstAgent.Type))
		loaded, err := backend.Read(configPath)
		if err != nil && !errors.Is(err, ErrPartialRead) {
			errMsg := fmt.Sprintf("read config %s: %v", configPath, err)
			log.Printf("load: %s", errMsg)
			loadErrs = append(loadErrs, errMsg)
			continue
		}
		if errors.Is(err, ErrPartialRead) {
			// 部分条目无法解析：保留可解析条目（只读路径）。写路径会在
			// writeToAgentLocked/removeFromAgentLocked 拒绝，避免重写丢数据。
			log.Printf("load: config %s partially readable (unparsable entries skipped)", configPath)
		}
		// 统计该配置实际检测到的服务器数（按归一化 key 去重，未纳入管理的
		// 条目也计入），供 Agent 页面显示"配置里有多少 MCP"。与 Scan 使用
		// 同一去重口径，保证扫描对话框的条目数与计数一致。
		detected := len(loaded)
		if detected > 0 {
			seen := make(map[string]struct{}, detected)
			for _, srv := range loaded {
				seen[scanDedupKey(srv)] = struct{}{}
			}
			detected = len(seen)
		}
		for _, agID := range agentIDs {
			configCounts[agID] = detected
		}
		for _, srv := range loaded {
			id := ensureGlobalID(srv.ID)
			managedID, ok := matchManagedID(baseline, id, srv)
			if !ok {
				// 未纳入管理的服务器：保留在配置文件中（写路径从磁盘合并），
				// 但不进入列表，等待用户通过扫描对话框显式加入。
				continue
			}
			now := time.Now().UTC().Format(time.RFC3339Nano)
			if existing, ok := servers[managedID]; ok {
				srv.InstalledAt = existing.InstalledAt
			} else if base, ok := baseline.byID[managedID]; ok && base.InstalledAt != "" {
				// 沿用上会话记录的安装时间，保持跨重启稳定
				srv.InstalledAt = base.InstalledAt
			} else {
				srv.InstalledAt = now
			}
			srv.UpdatedAt = now
			srv.ID = managedID
			servers[managedID] = srv
			// 绑定所有共享该 ConfigPath 的agent
			for _, agID := range agentIDs {
				recordBinding(bindings, managedID, agID)
			}
		}
	}

	var loadErr error
	if len(loadErrs) > 0 {
		loadErr = fmt.Errorf("load: %d config(s) failed: %s", len(loadErrs), strings.Join(loadErrs, "; "))
	}
	s.mu.Lock()
	oldServers := s.servers
	oldBindings := s.bindings
	oldCounts := s.configCounts
	s.servers = servers
	s.bindings = bindings
	s.configCounts = configCounts
	s.mergeDuplicatesLocked()
	snap := s.captureSyncSnapshotLocked()
	s.mu.Unlock()
	if dbErr := s.syncDBFromSnapshot(snap); dbErr != nil {
		// 数据库同步失败，回滚内存状态以防止不一致
		s.mu.Lock()
		s.servers = oldServers
		s.bindings = oldBindings
		s.configCounts = oldCounts
		s.loaded = false
		s.mu.Unlock()
		if loadErr != nil {
			return fmt.Errorf("%v; syncDB after load: %w", loadErr, dbErr)
		}
		return fmt.Errorf("syncDB after load: %w", dbErr)
	}
	s.mu.Lock()
	s.loaded = true
	s.mu.Unlock()
	return loadErr
}

// managedBaseline 表示上一会话同步进数据库的受管服务器集合。
// Load 以此为准：配置文件里存在但基线未匹配的服务器保持"未管理"，
// 只有用户通过扫描对话框显式加入（Add）后才会进入基线。
type managedBaseline struct {
	byID  map[string]Server // id → 服务器（含确定性 ID 与安装时间）
	byKey map[string]string // 归一化去重 key → id
}

// loadManagedBaseline 从数据库读取受管服务器作为 Load 的基线。
// 返回错误时由调用方按 Load 失败处理。
func (s *Store) loadManagedBaseline() (managedBaseline, error) {
	bl := managedBaseline{
		byID:  make(map[string]Server),
		byKey: make(map[string]string),
	}
	db := database.GetDB()
	if db == nil {
		return bl, fmt.Errorf("database not initialized")
	}
	rows, err := db.Query(`SELECT id, name, command, args, transport, url, installed_at FROM mcp_servers`)
	if err != nil {
		return bl, fmt.Errorf("query managed servers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id, name, command, transport, url string
			argsJSON                           sql.NullString
			installedAt                        int64
		)
		if err := rows.Scan(&id, &name, &command, &argsJSON, &transport, &url, &installedAt); err != nil {
			return bl, fmt.Errorf("scan managed server row: %w", err)
		}
		var args []string
		if argsJSON.Valid && argsJSON.String != "" {
			if err := json.Unmarshal([]byte(argsJSON.String), &args); err != nil {
				// args 损坏的行不作为基线（下一轮 sync 会清理该行）
				continue
			}
		}
		srv := Server{
			ID:          id,
			Name:        name,
			Command:     command,
			Args:        args,
			Transport:   Transport(transport),
			URL:         url,
			InstalledAt: time.Unix(installedAt, 0).UTC().Format(time.RFC3339Nano),
		}
		bl.byID[id] = srv
		if _, dup := bl.byKey[scanDedupKey(srv)]; !dup {
			bl.byKey[scanDedupKey(srv)] = id
		}
	}
	return bl, rows.Err()
}

// matchManagedID 判断配置文件中的服务器是否已被纳入管理，返回保持的 ID。
// 匹配顺序：
//  1. ID 相等（确定性 name@path ID，或上一会话保留的 UUID）；
//  2. 归一化去重 key 相等（命令/参数或 URL 一致）；
//  3. 同名唯一兜底——覆盖 AgentPack UI 修改命令导致 key 变化的重启场景；
//     同名的基线条目不唯一时不猜测，保持未管理。
func matchManagedID(bl managedBaseline, configID string, srv Server) (string, bool) {
	if _, ok := bl.byID[configID]; ok {
		return configID, true
	}
	if id, ok := bl.byKey[scanDedupKey(srv)]; ok {
		return id, true
	}
	if srv.Name == "" {
		return "", false
	}
	var picked string
	for id, b := range bl.byID {
		if strings.EqualFold(b.Name, srv.Name) {
			if picked != "" {
				return "", false
			}
			picked = id
		}
	}
	if picked != "" {
		return picked, true
	}
	return "", false
}

func recordBinding(bindings map[string]map[string]bool, serverID, agentID string) {
	if bindings[serverID] == nil {
		bindings[serverID] = make(map[string]bool)
	}
	bindings[serverID][agentID] = true
}

func (s *Store) mergeDuplicatesLocked() {
	// 与 scanDedupKey/Add/Update 的"已管理"判定共用同一归一化 key，
	// 保证加载期合并与会话期去重语义一致（URL 服务器按 URL 合并，而非全部塌缩）。
	keyToIDs := make(map[string][]string)
	for id, srv := range s.servers {
		key := scanDedupKey(srv)
		keyToIDs[key] = append(keyToIDs[key], id)
	}

	for _, ids := range keyToIDs {
		if len(ids) <= 1 {
			continue
		}
		// 按InstalledAt 升序排序，确保确定性选择最早安装的服务器
		sort.Slice(ids, func(i, j int) bool {
			ti := dbutil.ParseTimeToInt64(s.servers[ids[i]].InstalledAt)
			tj := dbutil.ParseTimeToInt64(s.servers[ids[j]].InstalledAt)
			if ti != tj {
				return ti < tj
			}
			return ids[i] < ids[j] // 时间相同时按 ID 字典序兜底
		})
		canonical := ids[0]
		for _, dupeID := range ids[1:] {
			for agentID := range s.bindings[dupeID] {
				s.recordBindingLocked(canonical, agentID)
			}
			delete(s.bindings, dupeID)
			delete(s.servers, dupeID)
		}
	}
}

// normalizeCommand 兼容 Windows 下cmd /c 包装的命令和 npx 包名差异。// 例如：//   cmd + ["/c", "npx", "-y", "pkg"]     → npx + ["-y", "pkg"]
//
//	npx + ["-y", "pkg@latest"]             → npx + ["-y", "pkg"]
func normalizeCommand(cmd string, args []string) (string, []string) {
	// 1. 剥离 Windows cmd /c 包装
	if cmd == "cmd" && len(args) >= 2 && args[0] == "/c" {
		cmd, args = args[1], args[2:]
	}
	// 2. 剥离 npx 包名的@latest 后缀（语义等价）
	if cmd == "npx" && len(args) > 0 {
		normalized := make([]string, len(args))
		for i, a := range args {
			normalized[i] = trimLatestSuffix(a)
		}
		args = normalized
	}
	return cmd, args
}

// trimLatestSuffix 去掉字符串末尾的 @latest 后缀
func trimLatestSuffix(s string) string {
	if len(s) > 7 && s[len(s)-7:] == "@latest" {
		return s[:len(s)-7]
	}
	return s
}

func (s *Store) recordBindingLocked(serverID, agentID string) {
	if s.bindings[serverID] == nil {
		s.bindings[serverID] = make(map[string]bool)
	}
	s.bindings[serverID][agentID] = true
}

// syncSnapshot 保存 syncDB 所需数据的快照，允许在锁外执行DB 写入
type syncSnapshot struct {
	servers  map[string]Server
	bindings map[string]map[string]bool
}

// captureSyncSnapshotLocked 快照当前 servers 和bindings（调用者需持锁）
func (s *Store) captureSyncSnapshotLocked() syncSnapshot {
	servers := make(map[string]Server, len(s.servers))
	for id, srv := range s.servers {
		// BoundAgents 不在 Server struct 中存储，需要从 bindings 派生
		// 深拷贝 Args 和 Env：range 仅复制 struct 值，切片/map 字段共享底层数据，
		// 快照在锁外被 syncDBFromSnapshot 消费，必须与内部状态完全隔离
		servers[id] = s.cloneForReturn(srv, id)
	}
	bindings := make(map[string]map[string]bool, len(s.bindings))
	for srvID, agents := range s.bindings {
		m := make(map[string]bool, len(agents))
		for agID := range agents {
			m[agID] = true
		}
		bindings[srvID] = m
	}
	return syncSnapshot{servers: servers, bindings: bindings}
}

func (s *Store) syncDBFromSnapshot(snap syncSnapshot) error {
	db := database.GetDB()
	if db == nil {
		return fmt.Errorf("database not initialized")
	}

	serverIDs := make([]string, 0, len(snap.servers))
	for id := range snap.servers {
		serverIDs = append(serverIDs, id)
	}
	bindingPairs := make([][2]string, 0)
	for srvID, agents := range snap.bindings {
		for agID := range agents {
			bindingPairs = append(bindingPairs, [2]string{srvID, agID})
		}
	}

	err := database.WithTransaction(func(tx *sql.Tx) error {
		now := time.Now().Unix()

		// 预编译语句以提高批量操作效率
		serverStmt, err := tx.Prepare(`INSERT OR REPLACE INTO mcp_servers (id, name, description, command, args, env, transport, config_type, url, timeout, source, source_id, installed_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return fmt.Errorf("prepare server statement: %w", err)
		}
		defer serverStmt.Close()

		bindStmt, err := tx.Prepare(`INSERT OR REPLACE INTO mcp_agent_bindings (mcp_id, agent_id, enabled, synced_at) VALUES (?,?,?,?)`)
		if err != nil {
			return fmt.Errorf("prepare binding statement: %w", err)
		}
		defer bindStmt.Close()

		for id, srv := range snap.servers {
			argsJSON, err := json.Marshal(srv.Args)
			if err != nil {
				return fmt.Errorf("marshal args for server %q: %w", srv.Name, err)
			}
			encryptedEnv, err := crypto.EncryptEnv(srv.Env)
			if err != nil {
				return fmt.Errorf("encrypt env for server %q: %w", srv.Name, err)
			}
			envJSON, err := json.Marshal(encryptedEnv)
			if err != nil {
				return fmt.Errorf("marshal env for server %q: %w", srv.Name, err)
			}
			installedAt := dbutil.ParseTimeToInt64(srv.InstalledAt)
			updatedAt := dbutil.ParseTimeToInt64(srv.UpdatedAt)
			if installedAt == 0 {
				installedAt = now
			}
			if updatedAt == 0 {
				updatedAt = now
			}
			if _, err := serverStmt.Exec(
				id, srv.Name, srv.Description, srv.Command, string(argsJSON), string(envJSON), string(srv.Transport), srv.ConfigType, srv.URL, srv.Timeout, srv.Source, srv.SourceID, installedAt, updatedAt,
			); err != nil {
				return err
			}
		}

		for _, pair := range bindingPairs {
			if _, err := bindStmt.Exec(
				pair[0], pair[1], 1, now,
			); err != nil {
				return err
			}
		}

		if len(serverIDs) > 0 {
			if _, err := tx.Exec(`DELETE FROM mcp_agent_bindings WHERE mcp_id NOT IN (`+dbutil.Placeholders(len(serverIDs))+`)`, dbutil.StrToIfaces(serverIDs)...); err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM mcp_servers WHERE id NOT IN (`+dbutil.Placeholders(len(serverIDs))+`)`, dbutil.StrToIfaces(serverIDs)...); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(`DELETE FROM mcp_agent_bindings`); err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM mcp_servers`); err != nil {
				return err
			}
		}

		return nil
	})

	if err != nil {
		log.Printf("syncDB transaction failed: %v", err)
	}
	return err
}

// AgentMcpCounts 返回每个 Agent 的 MCP 服务器数量。
// 优先使用最近一次 Load 从配置文件实际检测到的数量（含未纳入管理的条目，
// 反映"该 agent 配置里有多少 MCP"）；未在配置检测结果中的 agent
// （如 not_found 或旧数据）回退为绑定计数。
func (s *Store) AgentMcpCounts() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	counts := make(map[string]int)
	if len(s.configCounts) > 0 {
		for agID, c := range s.configCounts {
			counts[agID] = c
		}
		// 兜底：不在配置检测结果中的 agent 沿用绑定计数
		for _, agents := range s.bindings {
			for agID := range agents {
				if _, ok := counts[agID]; !ok {
					counts[agID]++
				}
			}
		}
		return counts
	}
	for _, agents := range s.bindings {
		for agID := range agents {
			counts[agID]++
		}
	}
	return counts
}

func (s *Store) List() []Server {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Server, 0, len(s.servers))
	for id, srv := range s.servers {
		out = append(out, s.cloneForReturn(srv, id))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Store) Get(id string) (Server, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	srv, ok := s.servers[id]
	if !ok {
		return Server{}, false
	}
	return s.cloneForReturn(srv, id), true
}

// Scan 重新读取所有 Agent 配置文件，返回已发现的所有 MCP 服务器，
// 并标记哪些已在 Store 中管理、哪些是新发现的。
// 按安装命令去重（command + args 归一化，忽略名称差异），
// URL 服务器（SSE/HTTP）按 URL 去重。
// 同一服务器在多个 agent 配置中存在时会合并为一个 ScanItem，
// Sources 字段保留所有来源 agent 信息。
func (s *Store) Scan(reg *agents.Registry) *ScanResult {
	items := make([]ScanItem, 0)
	managedKeys := make(map[string]bool)

	s.mu.RLock()
	for _, srv := range s.servers {
		managedKeys[scanDedupKey(srv)] = true
	}
	s.mu.RUnlock()

	pathGroups := make(map[string][]string)
	for _, ag := range reg.All() {
		if ag.ConfigPath == "" || ag.Status == agents.StatusNotFound {
			continue
		}
		pathGroups[ag.ConfigPath] = append(pathGroups[ag.ConfigPath], ag.ID)
	}

	// 按 configPath 排序，保证扫描顺序稳定，避免 map 遍历顺序随机
	// 导致同一 MCP 的"主来源"在不同次扫描中发生变化。
	configPaths := make([]string, 0, len(pathGroups))
	for cp := range pathGroups {
		configPaths = append(configPaths, cp)
	}
	sort.Strings(configPaths)

	// 已见 key → items 中的索引，便于把同一 MCP 的多个来源合并到同一 item
	seenIdx := make(map[string]int)
	failed := 0
	for _, configPath := range configPaths {
		agentIDs := pathGroups[configPath]
		firstAgent := reg.Get(agentIDs[0])
		if firstAgent == nil {
			continue
		}
		backend := NewBackend(string(firstAgent.Type))
		loaded, err := backend.Read(configPath)
		if err != nil && !errors.Is(err, ErrPartialRead) {
			failed++
			log.Printf("scan: read config %s: %v", configPath, err)
			continue
		}
		// 共享同一配置文件的多个 agent 都记录为来源
		sources := make([]ScanSource, 0, len(agentIDs))
		for _, agID := range agentIDs {
			if ag := reg.Get(agID); ag != nil {
				sources = append(sources, ScanSource{
					AgentID:    agID,
					AgentName:  ag.Name,
					ConfigPath: configPath,
				})
			}
		}
		if len(sources) == 0 {
			continue
		}
		for _, srv := range loaded {
			srv.ID = ensureGlobalID(srv.ID)
			key := scanDedupKey(srv)
			managed := managedKeys[key]
			if idx, ok := seenIdx[key]; ok {
				// 已存在同一 MCP，仅追加来源信息，不重复添加
				items[idx].Sources = append(items[idx].Sources, sources...)
				continue
			}
			seenIdx[key] = len(items)
			items = append(items, ScanItem{
				Server:     srv,
				Managed:    managed,
				AgentID:    sources[0].AgentID,
				AgentName:  sources[0].AgentName,
				ConfigPath: sources[0].ConfigPath,
				Sources:    sources,
			})
		}
	}

	managed := 0
	for _, item := range items {
		if item.Managed {
			managed++
		}
	}

	return &ScanResult{
		Items:    items,
		Total:    len(items),
		Managed:  managed,
		NewFound: len(items) - managed,
		Failed:   failed,
	}
}

// scanDedupKey 生成用于去重和"已管理"判断的唯一键。
// stdio 服务器：按归一化后的 command + args；
// SSE/HTTP/streamable-http 服务器：按 URL。
func scanDedupKey(srv Server) string {
	if srv.Transport == TransportSSE || srv.Transport == TransportHTTP || srv.Transport == TransportStreamableHTTP {
		return "url:" + srv.URL
	}
	cmd, args := normalizeCommand(srv.Command, srv.Args)
	return "cmd:" + cmd + "\x00" + strings.Join(append([]string{cmd}, args...), "\x00")
}

// findServerIDByKeyLocked 返回与 server 归一化 key 相同的已管理服务器 ID
// （排除 exclude 自身，无则空串）。调用者需持有写锁。
func (s *Store) findServerIDByKeyLocked(server Server, exclude string) string {
	key := scanDedupKey(server)
	for id, srv := range s.servers {
		if id == exclude {
			continue
		}
		if scanDedupKey(srv) == key {
			return id
		}
	}
	return ""
}

func (s *Store) ByAgent(agentID string) []Server {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Server, 0)
	for srvID, agents := range s.bindings {
		if !agents[agentID] {
			continue
		}
		if srv, ok := s.servers[srvID]; ok {
			out = append(out, s.cloneForReturn(srv, srvID))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Store) AgentBound(serverID, agentID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	agents, ok := s.bindings[serverID]
	if !ok {
		return false
	}
	return agents[agentID]
}

// FindByName 按名称查找 MCP 服务器，避免调用 List() 的全量拷贝开销。
// 返回第一个匹配的服务器。用于导入/恢复等需要按名称匹配的场景。
func (s *Store) FindByName(name string) (Server, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for id, srv := range s.servers {
		if srv.Name == name {
			return s.cloneForReturn(srv, id), true
		}
	}
	return Server{}, false
}

func (s *Store) boundAgentsLocked(serverID string) []string {
	agents, ok := s.bindings[serverID]
	if !ok {
		return []string{}
	}
	out := make([]string, 0, len(agents))
	for agID := range agents {
		out = append(out, agID)
	}
	sort.Strings(out)
	return out
}

func (s *Store) Add(server Server, agentIDs []string, reg *agents.Registry) (Server, error) {
	if err := validateServerName(server.Name); err != nil {
		return Server{}, err
	}
	if len(agentIDs) == 0 {
		return Server{}, fmt.Errorf("at least one agent required")
	}
	if err := validateAgentIDs(agentIDs, reg); err != nil {
		return Server{}, err
	}
	// 验证命令不包含危险的 shell 元字符）
	if server.Transport == TransportStdio {
		if err := ValidateCommand(server.Command); err != nil {
			return Server{}, err
		}
	}

	var notify MutationDetail
	var snap syncSnapshot
	var rollbackAgentIDs []string
	err := func() error {
		s.mu.Lock()
		defer s.mu.Unlock()

		now := time.Now().UTC().Format(time.RFC3339Nano)
		if server.ID == "" {
			server.ID = uuid.NewString()
		} else if _, exists := s.servers[server.ID]; exists {
			return fmt.Errorf("server with id %s already exists", server.ID)
		}
		// 拒绝重复管理：归一化 key（命令/参数或 URL）已存在的服务器应通过
		// ToggleAgent/Update 复用已有条目，避免 store 内出现同 key 双份。
		if existingID := s.findServerIDByKeyLocked(server, ""); existingID != "" {
			return fmt.Errorf("%w (id %s)", ErrDuplicateServer, existingID)
		}
		server.InstalledAt = now
		server.UpdatedAt = now

		oldConfigs, err := s.writeToAgentsLocked(server, agentIDs, reg)
		if err != nil {
			return err
		}

		s.servers[server.ID] = server
		for _, agID := range agentIDs {
			s.recordBindingLocked(server.ID, agID)
		}
		notify = MutationDetail{
			ServerID:   server.ID,
			ServerName: server.Name,
			Agents:     append([]string{}, agentIDs...),
			OldConfigs: oldConfigs,
		}
		rollbackAgentIDs = append([]string{}, agentIDs...)
		snap = s.captureSyncSnapshotLocked()
		return nil
	}()
	if err != nil {
		return Server{}, err
	}
	if dbErr := s.syncDBFromSnapshot(snap); dbErr != nil {
		if rollbackErr := s.rollbackAdd(server.ID, rollbackAgentIDs, notify.OldConfigs, reg); rollbackErr != nil {
			return Server{}, fmt.Errorf("sync database after add: %w; rollback: %v", dbErr, rollbackErr)
		}
		return Server{}, fmt.Errorf("sync database after add: %w", dbErr)
	}
	s.notify("mcp.add", notify)
	return server, nil
}

func (s *Store) Update(id string, server Server, agentIDs []string, reg *agents.Registry) error {
	if err := validateServerName(server.Name); err != nil {
		return err
	}
	if err := validateAgentIDs(agentIDs, reg); err != nil {
		return err
	}
	// 验证命令不包含危险的 shell 元字符）
	if server.Transport == TransportStdio {
		if err := ValidateCommand(server.Command); err != nil {
			return err
		}
	}
	var notify MutationDetail
	var snap syncSnapshot
	var rollbackOld Server
	var rollbackOldAgentIDs []string
	var rollbackAgentIDs []string
	err := func() error {
		s.mu.Lock()
		defer s.mu.Unlock()

		old, ok := s.servers[id]
		if !ok {
			return fmt.Errorf("server %s not found", id)
		}
		rollbackOld = old

		// 更新后的 key 若与其它服务器冲突则拒绝（排除自身），
		// 避免 store 内出现同 key 双份、下次 Load 时被静默合并。
		if collided := s.findServerIDByKeyLocked(server, id); collided != "" {
			return fmt.Errorf("%w with %s", ErrDuplicateServer, collided)
		}

		oldAgentIDs := make([]string, 0, len(s.bindings[id]))
		for agID := range s.bindings[id] {
			oldAgentIDs = append(oldAgentIDs, agID)
		}
		rollbackOldAgentIDs = append([]string{}, oldAgentIDs...)

		oldConfigs := make(map[string]string)
		if len(oldAgentIDs) > 0 {
			oc, err := s.removeFromAgentsLocked(old, oldAgentIDs, reg)
			if err != nil {
				if restoreErr := restoreConfigContents(oc); restoreErr != nil {
					return fmt.Errorf("%w; restore old configs: %v", err, restoreErr)
				}
				return err
			}
			for k, v := range oc {
				oldConfigs[k] = v
			}
		}
		// 只有在成功从 agents 移除后才删除内存绑定，避免配置文件与内存状态不一致）
		delete(s.bindings, id)

		server.ID = id
		server.InstalledAt = old.InstalledAt
		server.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)

		moreOld, err := s.writeToAgentsLocked(server, agentIDs, reg)
		if err != nil {
			if restoreErr := restoreConfigContents(oldConfigs); restoreErr != nil {
				err = fmt.Errorf("%w; restore old configs: %v", err, restoreErr)
			}
			// 写入新配置失败：尝试恢复旧绑定到内存和磁盘
			for _, agID := range oldAgentIDs {
				s.recordBindingLocked(id, agID)
			}
			return err
		}
		for k, v := range moreOld {
			oldConfigs[k] = v
		}
		s.servers[id] = server
		for _, agID := range agentIDs {
			s.recordBindingLocked(id, agID)
		}
		notify = MutationDetail{
			ServerID:   id,
			ServerName: server.Name,
			Agents:     append([]string{}, agentIDs...),
			OldServer:  &old,
			OldConfigs: oldConfigs,
		}
		rollbackAgentIDs = append([]string{}, agentIDs...)
		snap = s.captureSyncSnapshotLocked()
		return nil
	}()
	if err != nil {
		return err
	}
	if dbErr := s.syncDBFromSnapshot(snap); dbErr != nil {
		if rollbackErr := s.rollbackUpdate(id, rollbackOld, rollbackOldAgentIDs, rollbackAgentIDs, notify.OldConfigs, reg); rollbackErr != nil {
			return fmt.Errorf("sync database after update: %w; rollback: %v", dbErr, rollbackErr)
		}
		return fmt.Errorf("sync database after update: %w", dbErr)
	}
	s.notify("mcp.update", notify)
	return nil
}

func (s *Store) Remove(id string, reg *agents.Registry) error {
	var notify MutationDetail
	var snap syncSnapshot
	var rollbackServer Server
	var rollbackAgentIDs []string
	err := func() error {
		s.mu.Lock()
		defer s.mu.Unlock()

		srv, ok := s.servers[id]
		if !ok {
			return nil
		}
		rollbackServer = srv
		agentIDs := make([]string, 0, len(s.bindings[id]))
		for agID := range s.bindings[id] {
			agentIDs = append(agentIDs, agID)
		}
		rollbackAgentIDs = append([]string{}, agentIDs...)
		oldConfigs, err := s.removeFromAgentsLocked(srv, agentIDs, reg)
		if err != nil {
			if restoreErr := restoreConfigContents(oldConfigs); restoreErr != nil {
				return fmt.Errorf("%w; restore old configs: %v", err, restoreErr)
			}
			return err
		}
		delete(s.bindings, id)
		delete(s.servers, id)
		notify = MutationDetail{
			ServerID:   id,
			ServerName: srv.Name,
			Agents:     agentIDs,
			OldConfigs: oldConfigs,
		}
		snap = s.captureSyncSnapshotLocked()
		return nil
	}()
	if err != nil {
		return err
	}
	if notify.ServerID != "" {
		if dbErr := s.syncDBFromSnapshot(snap); dbErr != nil {
			if rollbackErr := s.rollbackRemove(notify.ServerID, rollbackServer, rollbackAgentIDs, notify.OldConfigs, reg); rollbackErr != nil {
				return fmt.Errorf("sync database after remove: %w; rollback: %v", dbErr, rollbackErr)
			}
			return fmt.Errorf("sync database after remove: %w", dbErr)
		}
		s.notify("mcp.remove", notify)
	}
	return nil
}

func (s *Store) ToggleAgent(id, agentID string, enabled bool, reg *agents.Registry) error {
	if err := validateAgentIDs([]string{agentID}, reg); err != nil {
		return err
	}
	var notify MutationDetail
	var snap syncSnapshot
	var rollbackWasBound bool
	var rollbackAgentID string
	err := func() error {
		s.mu.Lock()
		defer s.mu.Unlock()

		srv, ok := s.servers[id]
		if !ok {
			return fmt.Errorf("server %s not found", id)
		}
		ag := reg.Get(agentID)
		if ag == nil || ag.ConfigPath == "" {
			return fmt.Errorf("agent %s not found", agentID)
		}
		currentlyBound := s.bindings[id][agentID]
		rollbackWasBound = currentlyBound
		rollbackAgentID = agentID

		var oldConfigs map[string]string
		if enabled && !currentlyBound {
			oc, err := s.writeToAgentsLocked(srv, []string{agentID}, reg)
			if err != nil {
				return err
			}
			oldConfigs = oc
			s.recordBindingLocked(id, agentID)
		} else if !enabled && currentlyBound {
			oc, err := s.removeFromAgentsLocked(srv, []string{agentID}, reg)
			if err != nil {
				if restoreErr := restoreConfigContents(oc); restoreErr != nil {
					return fmt.Errorf("%w; restore old config: %v", err, restoreErr)
				}
				return err
			}
			oldConfigs = oc
			delete(s.bindings[id], agentID)
			if len(s.bindings[id]) == 0 {
				delete(s.bindings, id)
			}
		} else {
			return nil
		}
		notify = MutationDetail{
			ServerID:   id,
			ServerName: srv.Name,
			Agents:     []string{agentID},
			OldConfigs: oldConfigs,
		}
		snap = s.captureSyncSnapshotLocked()
		return nil
	}()
	if err != nil {
		return err
	}
	if notify.ServerID != "" {
		if dbErr := s.syncDBFromSnapshot(snap); dbErr != nil {
			if rollbackErr := s.rollbackToggle(notify.ServerID, rollbackAgentID, rollbackWasBound, notify.OldConfigs, reg); rollbackErr != nil {
				return fmt.Errorf("sync database after toggle: %w; rollback: %v", dbErr, rollbackErr)
			}
			return fmt.Errorf("sync database after toggle: %w", dbErr)
		}
		action := "mcp.unbind"
		if enabled {
			action = "mcp.bind"
		}
		s.notify(action, notify)
	}
	return nil
}

func (s *Store) rollbackAdd(serverID string, agentIDs []string, oldConfigs map[string]string, reg *agents.Registry) error {
	s.mu.Lock()
	delete(s.servers, serverID)
	delete(s.bindings, serverID)
	s.mu.Unlock()
	return restoreOrRemoveAgentConfigs(agentIDs, oldConfigs, reg)
}

func (s *Store) rollbackUpdate(id string, old Server, oldAgentIDs, newAgentIDs []string, oldConfigs map[string]string, reg *agents.Registry) error {
	s.mu.Lock()
	s.servers[id] = old
	delete(s.bindings, id)
	for _, agID := range oldAgentIDs {
		s.recordBindingLocked(id, agID)
	}
	s.mu.Unlock()
	return restoreOrRemoveAgentConfigs(unionStrings(oldAgentIDs, newAgentIDs), oldConfigs, reg)
}

func (s *Store) rollbackRemove(id string, srv Server, agentIDs []string, oldConfigs map[string]string, reg *agents.Registry) error {
	s.mu.Lock()
	s.servers[id] = srv
	delete(s.bindings, id)
	for _, agID := range agentIDs {
		s.recordBindingLocked(id, agID)
	}
	s.mu.Unlock()
	return restoreOrRemoveAgentConfigs(agentIDs, oldConfigs, reg)
}

func (s *Store) rollbackToggle(serverID, agentID string, wasBound bool, oldConfigs map[string]string, reg *agents.Registry) error {
	s.mu.Lock()
	if wasBound {
		s.recordBindingLocked(serverID, agentID)
	} else if s.bindings[serverID] != nil {
		delete(s.bindings[serverID], agentID)
	}
	s.mu.Unlock()
	return restoreOrRemoveAgentConfigs([]string{agentID}, oldConfigs, reg)
}

func restoreOrRemoveAgentConfigs(agentIDs []string, oldConfigs map[string]string, reg *agents.Registry) error {
	paths := make(map[string]bool)
	for _, agID := range agentIDs {
		ag := reg.Get(agID)
		if ag == nil || ag.ConfigPath == "" {
			continue
		}
		paths[ag.ConfigPath] = true
	}
	var errs []string
	for path := range paths {
		if data, ok := oldConfigs[path]; ok {
			if err := iowriter.WriteAtomic(path, []byte(data), 0600); err != nil {
				errs = append(errs, fmt.Sprintf("restore %s: %v", path, err))
			}
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Sprintf("remove created %s: %v", path, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func unionStrings(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, v := range append(append([]string{}, a...), b...) {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func (s *Store) writeToAgentsLocked(server Server, agentIDs []string, reg *agents.Registry) (map[string]string, error) {
	if err := validateAgentWritePaths(agentIDs, reg); err != nil {
		return nil, err
	}
	oldConfigs, backupErr := s.backupAgentsLocked(server, agentIDs, reg, true)
	if backupErr != nil {
		return oldConfigs, backupErr
	}
	// 按ConfigPath 去重，共享路径只写一次
	seen := make(map[string]bool)
	var writeErrs []string
	var writtenPaths []string
	createdPaths := make(map[string]bool)
	for _, agID := range agentIDs {
		ag := reg.Get(agID)
		if ag == nil || ag.ConfigPath == "" {
			continue
		}
		if seen[ag.ConfigPath] {
			continue
		}
		seen[ag.ConfigPath] = true
		if _, hadConfig := oldConfigs[ag.ConfigPath]; !hadConfig {
			if _, statErr := os.Stat(ag.ConfigPath); os.IsNotExist(statErr) {
				createdPaths[ag.ConfigPath] = true
			}
		}
		if err := s.writeToAgentLocked(server, agID, reg); err != nil {
			errMsg := fmt.Sprintf("write %s to agent %s: %v", server.Name, agID, err)
			log.Printf("writeToAgents: %s", errMsg)
			writeErrs = append(writeErrs, errMsg)
			continue
		}
		writtenPaths = append(writtenPaths, ag.ConfigPath)
	}
	if len(writtenPaths) == 0 && len(writeErrs) > 0 {
		return oldConfigs, fmt.Errorf("all agents failed: %s", strings.Join(writeErrs, "; "))
	}
	if len(writeErrs) > 0 {
		failedCount := len(writeErrs)
		if restoreErr := restoreWrittenAgentConfigs(writtenPaths, oldConfigs, createdPaths); restoreErr != nil {
			writeErrs = append(writeErrs, restoreErr.Error())
		}
		return oldConfigs, fmt.Errorf("%d/%d agents failed: %s", failedCount, len(agentIDs), strings.Join(writeErrs, "; "))
	}
	return oldConfigs, nil
}

func validateAgentWritePaths(agentIDs []string, reg *agents.Registry) error {
	seen := make(map[string]bool)
	var invalid []string
	for _, agID := range agentIDs {
		ag := reg.Get(agID)
		if ag == nil || ag.ConfigPath == "" {
			continue
		}
		if seen[ag.ConfigPath] {
			continue
		}
		seen[ag.ConfigPath] = true
		if !isSafeAgentConfigPath(ag.ConfigPath) {
			invalid = append(invalid, fmt.Sprintf("%s: %s", agID, ag.ConfigPath))
		}
	}
	if len(invalid) > 0 {
		return fmt.Errorf("invalid write path(s) outside the user profile: %s", strings.Join(invalid, "; "))
	}
	return nil
}

func restoreWrittenAgentConfigs(paths []string, oldConfigs map[string]string, createdPaths map[string]bool) error {
	seen := make(map[string]bool)
	var errs []string
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		if createdPaths[path] {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				errs = append(errs, fmt.Sprintf("remove created %s: %v", path, err))
			}
			continue
		}
		old, ok := oldConfigs[path]
		if !ok {
			continue
		}
		if err := iowriter.WriteAtomic(path, []byte(old), 0600); err != nil {
			errs = append(errs, fmt.Sprintf("restore %s: %v", path, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("rollback failed: %s", strings.Join(errs, "; "))
	}
	return nil
}

func restoreConfigContents(configs map[string]string) error {
	var errs []string
	for path, data := range configs {
		if err := iowriter.WriteAtomic(path, []byte(data), 0600); err != nil {
			errs = append(errs, fmt.Sprintf("restore %s: %v", path, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func (s *Store) removeFromAgentsLocked(server Server, agentIDs []string, reg *agents.Registry) (map[string]string, error) {
	if err := validateAgentWritePaths(agentIDs, reg); err != nil {
		return nil, err
	}
	oldConfigs, backupErr := s.backupAgentsLocked(server, agentIDs, reg, false)
	if backupErr != nil {
		return oldConfigs, backupErr
	}
	// 按ConfigPath 去重，共享路径只删一次
	seen := make(map[string]bool)
	var removeErrs []string
	for _, agID := range agentIDs {
		ag := reg.Get(agID)
		if ag == nil || ag.ConfigPath == "" {
			continue
		}
		if seen[ag.ConfigPath] {
			continue
		}
		seen[ag.ConfigPath] = true
		if err := s.removeFromAgentLocked(server, agID, reg); err != nil {
			errMsg := fmt.Sprintf("remove %s from agent %s: %v", server.Name, agID, err)
			log.Printf("removeFromAgents: %s", errMsg)
			removeErrs = append(removeErrs, errMsg)
			continue
		}
	}
	if len(removeErrs) > 0 {
		return oldConfigs, fmt.Errorf("remove: %d agent(s) failed: %s", len(removeErrs), strings.Join(removeErrs, "; "))
	}
	return oldConfigs, nil
}

// backupAgentsLocked 读取待写 agent 的配置原文并生成备份快照。
// skipIfSameNoop 指示本次操作对"配置文件已含同 key 条目"的路径是否无写入：
//   - 写路径传 true：采纳场景（同名同 key）不会改写文件，无需快照；
//   - 删路径传 false：非本 store 管理的同名条目不会删除，无需快照。
//
// oldConfigs 始终保留原文（供回滚恢复），仅跳过文件快照的生成。
func (s *Store) backupAgentsLocked(server Server, agentIDs []string, reg *agents.Registry, skipIfSameNoop bool) (map[string]string, error) {
	oldConfigs := make(map[string]string)
	seen := make(map[string]bool)
	for _, agID := range agentIDs {
		ag := reg.Get(agID)
		if ag == nil || ag.ConfigPath == "" {
			continue
		}
		if seen[ag.ConfigPath] {
			continue
		}
		seen[ag.ConfigPath] = true
		data, err := os.ReadFile(ag.ConfigPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return oldConfigs, fmt.Errorf("read %s: %w", ag.ConfigPath, err)
		}
		oldConfigs[ag.ConfigPath] = string(data)
		if server.Name != "" && configHasSameServer(server, ag) == skipIfSameNoop {
			continue
		}
		if _, err := BackupConfig(string(ag.Type), ag.ConfigPath); err != nil {
			return oldConfigs, fmt.Errorf("backup %s: %w", ag.ConfigPath, err)
		}
	}
	return oldConfigs, nil
}

// configHasSameServer 检查 agent 配置文件中是否已存在与 server 同 key 的条目。
// 读取失败视为 false（保守起见仍走备份路径）。
func configHasSameServer(server Server, ag *agents.Agent) bool {
	backend := NewBackend(string(ag.Type))
	current, err := backend.Read(ag.ConfigPath)
	if err != nil {
		return false
	}
	existing, ok := current[server.Name]
	if !ok {
		return false
	}
	return scanDedupKey(server) == scanDedupKey(existing)
}

func (s *Store) writeToAgentLocked(server Server, agentID string, reg *agents.Registry) error {
	ag := reg.Get(agentID)
	if ag == nil || ag.ConfigPath == "" {
		return fmt.Errorf("agent %s not found", agentID)
	}
	if !isSafeAgentConfigPath(ag.ConfigPath) {
		return fmt.Errorf("invalid write path: %s is outside the user profile", ag.ConfigPath)
	}
	backend := NewBackend(string(ag.Type))
	current, err := backend.Read(ag.ConfigPath)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if existing, ok := current[server.Name]; ok {
		managedExisting := false
		if srv, ok := s.servers[server.ID]; ok && srv.Name == server.Name {
			managedExisting = s.bindings[server.ID][agentID]
		}
		if !managedExisting {
			// 配置里已有同名条目且非本 store 管理。若归一化后是同一服务器
			// （命令/参数或 URL 一致，例如扫描到的"加入管理"场景），视为采纳
			// 已存在条目，跳过写入以保留用户原有配置格式；否则拒绝覆盖。
			if scanDedupKey(server) != scanDedupKey(existing) {
				return fmt.Errorf("server name %q already exists in agent %s", server.Name, agentID)
			}
			return nil
		}
	}
	current[server.Name] = server
	if err := backend.Write(ag.ConfigPath, current); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

func (s *Store) removeFromAgentLocked(server Server, agentID string, reg *agents.Registry) error {
	ag := reg.Get(agentID)
	if ag == nil || ag.ConfigPath == "" {
		return nil
	}
	if !isSafeAgentConfigPath(ag.ConfigPath) {
		return fmt.Errorf("invalid write path: %s is outside the user profile", ag.ConfigPath)
	}
	backend := NewBackend(string(ag.Type))
	current, err := backend.Read(ag.ConfigPath)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if existing, has := current[server.Name]; !has {
		return nil
	} else if scanDedupKey(server) != scanDedupKey(existing) {
		// 配置里的同名条目不是本 store 管理的服务器（用户手动改写/替换过），
		// 不删除，避免误删用户自己的配置。
		return nil
	}
	delete(current, server.Name)
	if err := backend.Write(ag.ConfigPath, current); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

func ensureGlobalID(id string) string {
	if id == "" {
		return uuid.NewString()
	}
	return id
}

func validateServerName(name string) error {
	if name == "" {
		return fmt.Errorf("server name required")
	}
	if len(name) > 128 {
		return fmt.Errorf("server name too long (max 128 characters)")
	}
	if !isValidName(name) {
		return fmt.Errorf("server name contains invalid characters (allowed: letters, digits, hyphens, underscores, spaces, dots, slashes)")
	}
	return nil
}

// isValidName checks that a server name only contains allowed characters.
func isValidName(name string) bool {
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == ' ' || c == '.' || c == '/') {
			return false
		}
	}
	return true
}

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

// isSafeAgentConfigPath verifies writes stay in the current user's profile.
// Agent config files live beside each agent's own settings, not under AgentPack.
func isSafeAgentConfigPath(path string) bool {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return false
	}
	resolved := resolvePathForWrite(clean)
	if os.Getenv("AGENTPACK_ALLOW_TEMP_DIR") == "1" {
		tempDir := filepath.Clean(os.TempDir())
		if rel, err := filepath.Rel(tempDir, resolved); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return true
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	home = filepath.Clean(home)
	if r, err := filepath.EvalSymlinks(home); err == nil {
		home = r
	}
	rel, err := filepath.Rel(home, resolved)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func resolvePathForWrite(path string) string {
	clean := filepath.Clean(path)
	if r, err := filepath.EvalSymlinks(clean); err == nil {
		return r
	}
	parts := []string{filepath.Base(clean)}
	dir := filepath.Dir(clean)
	for {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(append([]string{r}, parts...)...)
		}
		next := filepath.Dir(dir)
		if next == dir {
			return clean
		}
		parts = append([]string{filepath.Base(dir)}, parts...)
		dir = next
	}
}

// validateAgentIDs checks that all agent IDs exist in the registry and are enabled/detected.
func validateAgentIDs(agentIDs []string, reg *agents.Registry) error {
	var invalid []string
	for _, agID := range agentIDs {
		if ag := reg.Get(agID); ag == nil || (ag.Status != agents.StatusEnabled && ag.Status != agents.StatusDetected) {
			invalid = append(invalid, agID)
		}
	}
	if len(invalid) > 0 {
		return fmt.Errorf("invalid or inactive agent(s): %s", strings.Join(invalid, ", "))
	}
	return nil
}

// copySlice 返回字符串切片的深拷贝，防止返回值与内部状态共享底层数组）
func copySlice(src []string) []string {
	return shared.CopySlice(src)
}

// copyMap 返回 map 的深拷贝，防止返回值与内部状态共享引用）
func copyMap(src map[string]string) map[string]string {
	return shared.CopyMap(src)
}

// cloneForReturn 返回与 Store 内部状态完全隔离的 Server 副本，深拷贝
// BoundAgents、Args 和 Env，避免外部修改影响内部状态。id 用于查找 bindings。
func (s *Store) cloneForReturn(srv Server, id string) Server {
	srv.BoundAgents = copySlice(s.boundAgentsLocked(id))
	srv.Args = copySlice(srv.Args)
	srv.Env = copyMap(srv.Env)
	return srv
}
