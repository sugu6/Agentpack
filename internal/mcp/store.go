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

type Store struct {
	mu       sync.RWMutex
	servers  map[string]Server
	bindings map[string]map[string]bool
	hook     MutationHandler
}

func NewStore() *Store {
	return &Store{
		servers:  make(map[string]Server),
		bindings: make(map[string]map[string]bool),
	}
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
	servers := make(map[string]Server)
	bindings := make(map[string]map[string]bool)

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
		if err != nil {
			errMsg := fmt.Sprintf("read config %s: %v", configPath, err)
			log.Printf("load: %s", errMsg)
			loadErrs = append(loadErrs, errMsg)
			continue
		}
		for _, srv := range loaded {
			id := ensureGlobalID(srv.ID)
			srv.ID = id
			now := time.Now().UTC().Format(time.RFC3339Nano)
			if existing, ok := servers[id]; ok {
				srv.InstalledAt = existing.InstalledAt
			} else {
				srv.InstalledAt = now
			}
			srv.UpdatedAt = now
			servers[id] = srv
			// 绑定所有共享该 ConfigPath 的agent
			for _, agID := range agentIDs {
				recordBinding(bindings, id, agID)
			}
		}
	}

	if len(loadErrs) > 0 {
		return fmt.Errorf("load: %d config(s) failed: %s", len(loadErrs), strings.Join(loadErrs, "; "))
	}
	s.mu.Lock()
	oldServers := s.servers
	oldBindings := s.bindings
	s.servers = servers
	s.bindings = bindings
	s.mergeDuplicatesLocked()
	snap := s.captureSyncSnapshotLocked()
	s.mu.Unlock()
	if dbErr := s.syncDBFromSnapshot(snap); dbErr != nil {
		// 数据库同步失败，回滚内存状态以防止不一致
		s.mu.Lock()
		s.servers = oldServers
		s.bindings = oldBindings
		s.mu.Unlock()
		return fmt.Errorf("syncDB after load: %w", dbErr)
	}
	return nil
}

func recordBinding(bindings map[string]map[string]bool, serverID, agentID string) {
	if bindings[serverID] == nil {
		bindings[serverID] = make(map[string]bool)
	}
	bindings[serverID][agentID] = true
}

func (s *Store) mergeDuplicatesLocked() {
	type cmdKey struct {
		Command string
		Args    string
	}

	keyToIDs := make(map[cmdKey][]string)
	for id, srv := range s.servers {
		cmd, args := normalizeCommand(srv.Command, srv.Args)
		argsJSON, _ := json.Marshal(args)
		k := cmdKey{Command: cmd, Args: string(argsJSON)}
		keyToIDs[k] = append(keyToIDs[k], id)
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

func (s *Store) AgentMcpCounts() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	counts := make(map[string]int)
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

	seenKeys := make(map[string]bool)
	for configPath, agentIDs := range pathGroups {
		firstAgent := reg.Get(agentIDs[0])
		if firstAgent == nil {
			continue
		}
		backend := NewBackend(string(firstAgent.Type))
		loaded, err := backend.Read(configPath)
		if err != nil {
			log.Printf("scan: read config %s: %v", configPath, err)
			continue
		}
		for _, srv := range loaded {
			srv.ID = ensureGlobalID(srv.ID)
			key := scanDedupKey(srv)
			managed := managedKeys[key]
			agentID := agentIDs[0]
			ag := reg.Get(agentID)
			agentName := ""
			if ag != nil {
				agentName = ag.Name
			}
			if seenKeys[key] {
				continue
			}
			seenKeys[key] = true
			items = append(items, ScanItem{
				Server:     srv,
				Managed:    managed,
				AgentID:    agentID,
				AgentName:  agentName,
				ConfigPath: configPath,
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
	}
}

// scanDedupKey 生成用于去重和"已管理"判断的唯一键。
// stdio 服务器：按归一化后的 command + args；
// SSE/HTTP 服务器：按 URL。
func scanDedupKey(srv Server) string {
	if srv.Transport == TransportSSE || srv.Transport == TransportHTTP {
		return "url:" + srv.URL
	}
	cmd, args := normalizeCommand(srv.Command, srv.Args)
	return "cmd:" + cmd + "\x00" + strings.Join(append([]string{cmd}, args...), "\x00")
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
	oldConfigs, backupErr := s.backupAgentsLocked(agentIDs, reg)
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
	oldConfigs, backupErr := s.backupAgentsLocked(agentIDs, reg)
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

func (s *Store) backupAgentsLocked(agentIDs []string, reg *agents.Registry) (map[string]string, error) {
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
		if _, err := BackupConfig(string(ag.Type), ag.ConfigPath); err != nil {
			return oldConfigs, fmt.Errorf("backup %s: %w", ag.ConfigPath, err)
		}
	}
	return oldConfigs, nil
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
	if _, has := current[server.Name]; !has {
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
