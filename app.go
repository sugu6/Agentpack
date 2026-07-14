package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"agentpack/internal/agents"
	"agentpack/internal/backup"
	"agentpack/internal/config"
	"agentpack/internal/database"
	"agentpack/internal/market"
	"agentpack/internal/mcp"
	"agentpack/internal/skills"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx       context.Context
	mu        sync.RWMutex // 保护 App 内部状态（registry, stores, cfg）
	rescanMu  sync.Mutex   // 序列化 RescanAgents（先于 storeOpMu 获取）
	storeOpMu sync.Mutex   // 序列化 MCP/Skills 存储操作（后于 rescanMu）
	// ⚠️ 锁定顺序约定（违反将导致死锁）：
	//   1. rescanMu (仅在 RescanAgents 中获取)
	//   2. storeOpMu
	//   3. a.mu
	// Go vet 建议：新增方法若需要多种锁，请严格遵循此顺序。
	cfg           *config.AppConfig
	registry      *agents.Registry
	mcpStore      *mcp.Store
	mcpStoreReady bool
	mcpStoreErr   string
	skillsStore   *skills.Store
	marketStore   *market.Store
	backups       *backup.Manager
	exporter      *backup.Exporter
	closed        bool
	allowClose    bool
	trayActive    bool
	startupErrors []string
	inFlight       int
	flightCond     *sync.Cond
	downloadCancel context.CancelFunc
}

func NewApp(cfg *config.AppConfig) *App {
	a := &App{cfg: cfg}
	a.flightCond = sync.NewCond(&a.mu)
	return a
}

func (a *App) startup(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ctx = ctx

	var errs []string
	addErr := func(stage string, err error) {
		if err != nil {
			log.Printf("%s: %v", stage, err)
			errs = append(errs, fmt.Sprintf("%s: %v", stage, err))
		}
	}

	if err := os.MkdirAll(config.AgentPackDir(), 0700); err != nil {
		addErr("create agentpack dir", err)
	}

	if cfgErr := config.LastLoadError(); cfgErr != nil {
		addErr("config load", cfgErr)
	}

	dbPath := filepath.Join(config.AgentPackDir(), "agentpack.db")
	if err := database.Init(dbPath); err != nil {
		addErr("database init", err)
	}

	switch a.cfg.Settings.Theme {
	case "dark":
		wruntime.WindowSetDarkTheme(ctx)
	case "light":
		wruntime.WindowSetLightTheme(ctx)
	default:
		wruntime.WindowSetSystemDefaultTheme(ctx)
	}

	a.registry = agents.NewRegistry()
	a.registry.Scan()
	a.registry.LoadDisabled(a.cfg.DisabledAgents)

	a.mcpStore = mcp.NewStore()
	if err := a.mcpStore.Load(a.registry); err != nil {
		addErr("mcp store load", err)
		a.mcpStoreReady = false
		a.mcpStoreErr = err.Error()
	} else {
		a.mcpStoreReady = true
		a.mcpStoreErr = ""
	}

	a.registry.UpdateCounts(a.mcpStore.AgentMcpCounts())

	ssotDir := skills.ResolveSSOTDir(skills.StorageLocation(a.cfg.Settings.SkillStorage))
	a.skillsStore = skills.NewStore(ssotDir, skills.SyncMethod(a.cfg.Settings.SkillSyncMethod))
	if err := a.skillsStore.Load(a.registry); err != nil {
		addErr("skills store load", err)
	}

	a.marketStore = market.NewStore("")
	a.marketStore.RegisterServer(market.NewOfficialFetcher())
	a.marketStore.RegisterServer(market.NewSmitheryFetcher())

	// 注册 Skill fetchers
	a.marketStore.RegisterSkillFetcher(market.NewGitHubSkillFetcher(func() []market.RepoRef {
		// 从当前配置读取仓库列表（App 可能随时更新配置）
		a.mu.RLock()
		defer a.mu.RUnlock()
		if a.cfg == nil {
			return nil
		}
		refs := make([]market.RepoRef, 0, len(a.cfg.Settings.SkillRepos))
		for _, r := range a.cfg.Settings.SkillRepos {
			refs = append(refs, market.RepoRef{Owner: r.Owner, Name: r.Name, Branch: r.Branch})
		}
		return refs
	}))
	a.marketStore.RegisterSkillFetcher(market.NewSkillsShFetcher())

	a.backups = backup.NewManager(config.AgentPackDir(), a.cfg.Settings.BackupRetention, a.registry)
	a.backups.Bind(a.registry, a.mcpStore)
	a.exporter = backup.NewExporter(a.mcpStore, a.registry)
	a.setConfigProviders()

	a.refreshBackupHooksLocked()

	a.startupErrors = errs

	// 启动系统托盘（在 goroutine 中运行）
	a.trayActive = true
	go setupTray(a)

	// 启动 5 秒后静默检查更新
	go func() {
		time.Sleep(5 * time.Second)
		a.mu.RLock()
		if a.closed {
			a.mu.RUnlock()
			return
		}
		ctx := a.ctx
		a.mu.RUnlock()
		result, err := a.CheckUpdate()
		if err == nil && result.HasUpdate {
			wruntime.EventsEmit(ctx, "app:update-available", result)
		}
	}()
}

func (a *App) shutdown(ctx context.Context) {
	// 清理系统托盘
	if a.trayActive {
		cleanupTray()
		a.trayActive = false
	}
	a.mu.Lock()
	a.closed = true
	if a.inFlight > 0 {
		// 后台 goroutine 在超时后强制 Broadcast，避免 Wait() 在任务挂起时永久阻塞
		// close(done) 必须在 Unlock() 之前调用，确保主循环重新获取 a.mu 时 done 已关闭，
		// 否则主循环可能命中 select 的 default 分支并再次 Wait()，而 goroutine 已退出不再 Broadcast
		done := make(chan struct{})
		go func() {
			select {
			case <-time.After(5 * time.Second):
				a.mu.Lock()
				a.flightCond.Broadcast()
				close(done)
				a.mu.Unlock()
			case <-done:
				return
			}
		}()
		for a.inFlight > 0 {
			a.flightCond.Wait()
			select {
			case <-done:
				log.Printf("shutdown: timeout waiting for %d in-flight tasks", a.inFlight)
				goto waitDone
			default:
			}
		}
	waitDone:
		a.mu.Unlock()
		<-done
	} else {
		a.mu.Unlock()
	}

	if a.backups != nil {
		done := make(chan struct{})
		go func() {
			a.backups.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			log.Printf("shutdown: timeout waiting for backup hooks")
		}
	}

	if err := database.Close(); err != nil {
		log.Printf("database close: %v", err)
	}
}

func (a *App) beforeClose(ctx context.Context) bool {
	a.mu.RLock()
	closed := a.closed
	inFlight := a.inFlight
	allowClose := a.allowClose
	a.mu.RUnlock()

	if closed {
		return false
	}
	// allowClose 为 true 时，说明用户已确认退出，放行关闭
	if allowClose {
		return false
	}
	if inFlight > 0 {
		wruntime.EventsEmit(ctx, "app:close-blocked", nil)
		return true
	}
	// 发出关闭请求事件，前端根据配置决定行为
	wruntime.EventsEmit(ctx, "app:close-requested", nil)
	return true
}

func (a *App) beginInFlight() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("app is shutting down")
	}
	a.inFlight++
	return nil
}

func (a *App) endInFlight() {
	a.mu.Lock()
	if a.inFlight > 0 {
		a.inFlight--
	}
	a.flightCond.Broadcast()
	a.mu.Unlock()
}

func (a *App) ready() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return !a.closed && a.ctx != nil
}

func (a *App) emit(event string, data ...interface{}) {
	if !a.ready() {
		return
	}
	wruntime.EventsEmit(a.ctx, event, data...)
}

func (a *App) emitLocked(event string, data ...interface{}) {
	if a.closed || a.ctx == nil {
		return
	}
	wruntime.EventsEmit(a.ctx, event, data...)
}

func (a *App) refreshBackupHooksLocked() {
	if a.mcpStore == nil {
		return
	}
	if a.backups == nil || (a.cfg != nil && !a.cfg.Settings.AutoBackup) {
		a.mcpStore.SetMutationHandler(nil)
		return
	}
	a.mcpStore.SetMutationHandler(backup.MCPMutationHook(a.backups))
}

// setConfigProviders 为备份管理器和导出器设置应用设置的读取回调，
// 使快照/导出包含完整的应用设置（主题、备份配置、技能仓库等）。
func (a *App) setConfigProviders() {
	provider := func() map[string]any {
		a.mu.RLock()
		defer a.mu.RUnlock()
		if a.cfg == nil {
			return nil
		}
		data, err := json.Marshal(a.cfg.Settings)
		if err != nil {
			return nil
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			return nil
		}
		return m
	}
	if a.backups != nil {
		a.backups.SetSettingsProvider(provider)
	}
	if a.exporter != nil {
		a.exporter.SetSettingsProvider(provider)
	}
}

func (a *App) emitAgentsChangedLocked() {
	if a.registry == nil {
		return
	}
	if a.mcpStore != nil {
		a.registry.UpdateCounts(a.mcpStore.AgentMcpCounts())
	}
	a.emitLocked("agents:changed", a.registry.All())
}

func (a *App) assertInit() error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		return fmt.Errorf("app is shutting down")
	}
	if a.registry == nil || a.mcpStore == nil || a.marketStore == nil {
		return fmt.Errorf("app not initialized")
	}
	return nil
}

func (a *App) snapshot() (reg *agents.Registry, ms *mcp.Store, mks *market.Store, ss *skills.Store, backups *backup.Manager, exporter *backup.Exporter) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.registry, a.mcpStore, a.marketStore, a.skillsStore, a.backups, a.exporter
}

func (a *App) ListAgents() ([]*agents.Agent, error) {
	reg, ms, _, _, _, _ := a.snapshot()
	if reg == nil {
		return []*agents.Agent{}, nil
	}
	if ms != nil {
		reg.UpdateCounts(ms.AgentMcpCounts())
	}
	return reg.All(), nil
}

func (a *App) RescanAgents() ([]*agents.Agent, error) {
	if err := a.assertInit(); err != nil {
		return nil, err
	}
	if err := a.beginInFlight(); err != nil {
		return nil, err
	}
	defer a.endInFlight()

	a.rescanMu.Lock()
	defer a.rescanMu.Unlock()

	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()

	var disabledIDs []string
	var skillStorage string
	var skillSyncMethod string
	func() {
		a.mu.RLock()
		defer a.mu.RUnlock()
		disabledIDs = a.registry.DisabledIDs()
		skillStorage = a.cfg.Settings.SkillStorage
		skillSyncMethod = a.cfg.Settings.SkillSyncMethod
	}()

	ssotDir := skills.ResolveSSOTDir(skills.StorageLocation(skillStorage))

	// 在释放 a.mu 之前完成耗时的 I/O 操作
	newReg := agents.NewRegistry()
	newReg.Scan()
	newReg.LoadDisabled(disabledIDs)

	newMcpStore := mcp.NewStore()
	if err := newMcpStore.Load(newReg); err != nil {
		return nil, fmt.Errorf("mcp reload: %w", err)
	}

	newSkillsStore := skills.NewStore(ssotDir, skills.SyncMethod(skillSyncMethod))
	if err := newSkillsStore.Load(newReg); err != nil {
		return nil, fmt.Errorf("skills reload: %w", err)
	}

	// 只在最后更新共享状态时持有 a.mu
	newReg.UpdateCounts(newMcpStore.AgentMcpCounts())
	all := newReg.All()

	a.mu.Lock()
	a.registry = newReg
	a.mcpStore = newMcpStore
	a.mcpStoreReady = true
	a.mcpStoreErr = ""
	a.skillsStore = newSkillsStore
	a.backups.Bind(a.registry, a.mcpStore)
	a.exporter = backup.NewExporter(a.mcpStore, a.registry)
	a.setConfigProviders()
	a.refreshBackupHooksLocked()
	a.emitLocked("agents:changed", all)
	a.emitLocked("mcp:changed", a.mcpStore.List())
	a.emitLocked("skills:changed", a.skillsStore.List())
	a.mu.Unlock()

	return all, nil
}

func (a *App) GetAgent(id string) (*agents.Agent, error) {
	reg, _, _, _, _, _ := a.snapshot()
	if reg == nil {
		return nil, nil
	}
	return reg.Get(id), nil
}

func (a *App) ToggleAgent(id string, enabled bool) error {
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.registry == nil {
		return fmt.Errorf("registry not initialized")
	}
	if a.closed {
		return fmt.Errorf("app is shutting down")
	}
	oldDisabled := append([]string{}, a.cfg.DisabledAgents...)
	a.registry.Toggle(id, enabled)
	a.cfg.DisabledAgents = a.registry.DisabledIDs()
	if err := config.Save(a.cfg); err != nil {
		a.cfg.DisabledAgents = oldDisabled
		a.registry.ApplyDisabled(oldDisabled)
		return err
	}
	a.emitLocked("agents:changed", a.registry.All())
	return nil
}

func (a *App) requireMcpStoreReadyLocked() error {
	if a.mcpStore == nil {
		return fmt.Errorf("mcp store not initialized")
	}
	if !a.mcpStoreReady {
		if a.mcpStoreErr != "" {
			return fmt.Errorf("mcp store not loaded: %s", a.mcpStoreErr)
		}
		return fmt.Errorf("mcp store not loaded")
	}
	return nil
}

func (a *App) ListMcpServers() ([]mcp.Server, error) {
	_, ms, _, _, _, _ := a.snapshot()
	if ms == nil {
		return []mcp.Server{}, nil
	}
	return ms.List(), nil
}

func (a *App) ScanMcpServers() (*mcp.ScanResult, error) {
	reg, ms, _, _, _, _ := a.snapshot()
	if reg == nil {
		return nil, fmt.Errorf("registry not initialized")
	}
	if ms == nil {
		return nil, fmt.Errorf("mcp store not initialized")
	}
	return ms.Scan(reg), nil
}

func (a *App) GetMcpServer(id string) (mcp.Server, error) {
	_, ms, _, _, _, _ := a.snapshot()
	if ms == nil {
		return mcp.Server{}, fmt.Errorf("store not initialized")
	}
	srv, ok := ms.Get(id)
	if !ok {
		return mcp.Server{}, fmt.Errorf("server %s not found", id)
	}
	return srv, nil
}

func (a *App) GetAgentMcpServers(agentID string) ([]mcp.Server, error) {
	_, ms, _, _, _, _ := a.snapshot()
	if ms == nil {
		return []mcp.Server{}, nil
	}
	return ms.ByAgent(agentID), nil
}

func (a *App) AddMcpServer(server mcp.Server, agentIDs []string) error {
	if err := a.assertInit(); err != nil {
		return err
	}
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("app is shutting down")
	}
	if err := a.requireMcpStoreReadyLocked(); err != nil {
		return err
	}
	if _, err := a.mcpStore.Add(server, agentIDs, a.registry); err != nil {
		return err
	}
	a.emitAgentsChangedLocked()
	a.emitLocked("mcp:changed", a.mcpStore.List())
	return nil
}

func (a *App) UpdateMcpServer(id string, server mcp.Server, agentIDs []string) error {
	if err := a.assertInit(); err != nil {
		return err
	}
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("app is shutting down")
	}
	if err := a.requireMcpStoreReadyLocked(); err != nil {
		return err
	}
	if err := a.mcpStore.Update(id, server, agentIDs, a.registry); err != nil {
		return err
	}
	a.emitAgentsChangedLocked()
	a.emitLocked("mcp:changed", a.mcpStore.List())
	return nil
}

func (a *App) DeleteMcpServer(id string) error {
	if err := a.assertInit(); err != nil {
		return err
	}
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("app is shutting down")
	}
	if err := a.requireMcpStoreReadyLocked(); err != nil {
		return err
	}
	if err := a.mcpStore.Remove(id, a.registry); err != nil {
		return err
	}
	a.emitAgentsChangedLocked()
	a.emitLocked("mcp:changed", a.mcpStore.List())
	return nil
}

func (a *App) ToggleMcpServerAgent(id, agentID string, enabled bool) error {
	if err := a.assertInit(); err != nil {
		return err
	}
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("app is shutting down")
	}
	if err := a.requireMcpStoreReadyLocked(); err != nil {
		return err
	}
	if err := a.mcpStore.ToggleAgent(id, agentID, enabled, a.registry); err != nil {
		return err
	}
	a.emitAgentsChangedLocked()
	a.emitLocked("mcp:changed", a.mcpStore.List())
	return nil
}

func (a *App) SearchMarketServers(source, query, cursor string, pageSize int) (*market.SearchResultServers, error) {
	_, _, mks, _, _, _ := a.snapshot()
	if mks == nil {
		return nil, fmt.Errorf("market store not initialized")
	}
	ctx, cancel := market.ContextWithTimeout(15 * time.Second)
	defer cancel()
	return mks.Search(ctx, market.Source(source), market.SearchOptions{
		Query:    query,
		PageSize: pageSize,
		Cursor:   cursor,
	})
}

func (a *App) GetMarketServer(source, sourceID string) (market.MarketServer, error) {
	_, _, mks, _, _, _ := a.snapshot()
	if mks == nil {
		return market.MarketServer{}, fmt.Errorf("market store not initialized")
	}
	ctx, cancel := market.ContextWithTimeout(15 * time.Second)
	defer cancel()
	srv, err := mks.GetServer(ctx, market.Source(source), sourceID)
	if err != nil {
		return market.MarketServer{}, err
	}
	return *srv, nil
}

func (a *App) InstallMarketServer(server market.MarketServer, agentIDs []string) (mcp.Server, error) {
	if err := a.assertInit(); err != nil {
		return mcp.Server{}, err
	}
	if err := a.beginInFlight(); err != nil {
		return mcp.Server{}, err
	}
	defer a.endInFlight()
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	if server.Name == "" {
		return mcp.Server{}, fmt.Errorf("server name required")
	}
	if server.Command == "" && server.URL == "" {
		return mcp.Server{}, fmt.Errorf("server must have command or url")
	}
	env := server.Env
	if env == nil {
		env = map[string]string{}
	}
	transport := server.Transport
	if transport == "" {
		transport = "stdio"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return mcp.Server{}, fmt.Errorf("app is shutting down")
	}
	if err := a.requireMcpStoreReadyLocked(); err != nil {
		return mcp.Server{}, err
	}
	created, err := a.mcpStore.Add(mcp.Server{
		Name:        server.Name,
		Description: server.Description,
		Command:     server.Command,
		Args:        server.Args,
		Env:         env,
		Transport:   mcp.Transport(transport),
		URL:         server.URL,
		Source:      string(server.Source),
		SourceID:    server.SourceID,
	}, agentIDs, a.registry)
	if err != nil {
		return mcp.Server{}, err
	}
	a.emitAgentsChangedLocked()
	a.emitLocked("mcp:changed", a.mcpStore.List())
	return created, nil
}

// SearchMarketSkills 搜索市场中的 Skills，合并所有来源并按下载量排序
// SearchMarketSkills 搜索市场 skills
// source 参数："" 表示搜索全部启用的来源，"github" 仅 GitHub 仓库，"skills-sh" 仅 skills.sh
// page 从 1 开始，支持分页（无限滚动）
func (a *App) SearchMarketSkills(query string, pageSize int, page int, source string) (*market.SearchResultSkills, error) {
	_, _, mks, _, _, _ := a.snapshot()
	if mks == nil {
		return nil, fmt.Errorf("market store not initialized")
	}
	// 在锁内读取配置，避免数据竞争
	var enabledSources []market.Source
	a.mu.RLock()
	if a.cfg != nil && a.cfg.Settings.MarketSources != nil {
		if ms, ok := a.cfg.Settings.MarketSources["github"]; ok && ms.Enabled {
			enabledSources = append(enabledSources, market.SourceGitHub)
		}
		if ms, ok := a.cfg.Settings.MarketSources["skills-sh"]; ok && ms.Enabled {
			enabledSources = append(enabledSources, market.SourceSkillsSh)
		}
	}
	a.mu.RUnlock()
	// 前端指定了来源时，只搜索该来源
	if source != "" {
		var filtered []market.Source
		for _, s := range enabledSources {
			if string(s) == source {
				filtered = append(filtered, s)
			}
		}
		enabledSources = filtered
	}
	log.Printf("SearchMarketSkills: query=%q pageSize=%d page=%d source=%q enabledSources=%v", query, pageSize, page, source, enabledSources)
	// Skills 搜索可能需要扫描多个 GitHub 仓库（每个仓库含多个 SKILL.md），超时设长一些
	ctx, cancel := market.ContextWithTimeout(120 * time.Second)
	defer cancel()
	result, err := mks.SearchAllSkills(ctx, market.SearchOptions{
		Query:    query,
		PageSize: pageSize,
		Page:     page,
	}, enabledSources)
	if err != nil {
		log.Printf("SearchMarketSkills: SearchAllSkills error: %v", err)
		return nil, err
	}
	log.Printf("SearchMarketSkills: result total=%d items=%d hasMore=%v nextPage=%q", result.Total, len(result.Items), result.HasMore, result.NextPage)
	return result, nil
}

// InstallMarketSkill 从远程仓库 tarball 安装 skill 到指定 agents
func (a *App) InstallMarketSkill(skill market.MarketSkill, agentIDs []string) (skills.Skill, error) {
	if err := a.assertInit(); err != nil {
		return skills.Skill{}, err
	}
	if err := a.beginInFlight(); err != nil {
		return skills.Skill{}, err
	}
	defer a.endInFlight()
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()

	if skill.Directory == "" {
		return skills.Skill{}, fmt.Errorf("skill directory required")
	}
	if skill.RepoOwner == "" || skill.RepoName == "" {
		return skills.Skill{}, fmt.Errorf("skill repo owner/name required")
	}
	branch := skill.RepoBranch
	if branch == "" {
		branch = "main"
	}

	// 构造 tarball URL
	tarballURL := fmt.Sprintf("https://codeload.github.com/%s/%s/tar.gz/refs/heads/%s",
		skill.RepoOwner, skill.RepoName, branch)

	reg, _, _, ss, _, _ := a.snapshot()
	if ss == nil || reg == nil {
		return skills.Skill{}, fmt.Errorf("skills store or registry not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	input := skills.TarballInstallInput{
		TarballURL: tarballURL,
		Directory:  skill.Directory,
		FullPath:   skill.FullPath, // 传递完整相对路径（如 "skills/pdf"），安装时精准定位
		RepoOwner:  skill.RepoOwner,
		RepoName:   skill.RepoName,
		RepoBranch: branch,
	}
	installed, err := ss.InstallFromTarball(ctx, input, agentIDs, reg)
	if err != nil {
		return skills.Skill{}, err
	}

	// 写入 ~/.agents/.skill-lock.json（兼容 CC Switch 等工具）
	lockEntry := skills.AgentsLockEntry{
		Directory:  skill.Directory,
		Source:     skill.RepoOwner + "/" + skill.RepoName,
		SourceType: "github",
		SourceURL:  "https://github.com/" + skill.RepoOwner + "/" + skill.RepoName,
		SkillPath:  filepath.Join(ss.SSOTDir(), skill.Directory),
		Branch:     branch,
	}
	if err := skills.WriteAgentsLock(lockEntry); err != nil {
		// 锁文件写入失败不阻断安装，仅记录日志
		log.Printf("warning: write agents lock for %s: %v", skill.Directory, err)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.emitAgentsChangedLocked()
	a.emitLocked("skills:changed", ss.List())
	// 安装成功后异步缓存 Tree SHA 作为更新检测基线
	go func() {
		_ = skills.CacheSkillTreeSHA(installed.ID, input.RepoOwner, input.RepoName,
			input.RepoBranch, input.Directory)
	}()
	return installed, nil
}

// GetSkillRepos 获取当前配置的 GitHub 仓库扫描列表
func (a *App) GetSkillRepos() ([]config.SkillRepo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.cfg == nil {
		return nil, nil
	}
	// 返回副本，避免外部修改
	out := make([]config.SkillRepo, len(a.cfg.Settings.SkillRepos))
	copy(out, a.cfg.Settings.SkillRepos)
	return out, nil
}

// AddSkillRepo 添加一个 GitHub 仓库到扫描列表
func (a *App) AddSkillRepo(repo config.SkillRepo) error {
	if err := a.assertInit(); err != nil {
		return err
	}
	if repo.Owner == "" || repo.Name == "" {
		return fmt.Errorf("repo owner and name required")
	}
	if repo.Branch == "" {
		repo.Branch = "main"
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg == nil {
		return fmt.Errorf("config not loaded")
	}
	// 检查重复
	for _, r := range a.cfg.Settings.SkillRepos {
		if r.Owner == repo.Owner && r.Name == repo.Name {
			return fmt.Errorf("repo %s/%s already exists", repo.Owner, repo.Name)
		}
	}
	a.cfg.Settings.SkillRepos = append(a.cfg.Settings.SkillRepos, repo)
	// 保存配置
	if err := config.Save(a.cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	// 清理 skills 市场缓存，确保下次搜索能扫描到新添加的仓库
	if a.marketStore != nil {
		n, err := a.marketStore.ClearAllCache()
		log.Printf("AddSkillRepo: cleared %d cache files, err=%v", n, err)
	}
	return nil
}

// RemoveSkillRepo 从扫描列表移除一个 GitHub 仓库
func (a *App) RemoveSkillRepo(repo config.SkillRepo) error {
	if err := a.assertInit(); err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg == nil {
		return fmt.Errorf("config not loaded")
	}
	// 查找并删除
	found := false
	updated := a.cfg.Settings.SkillRepos[:0]
	for _, r := range a.cfg.Settings.SkillRepos {
		if r.Owner == repo.Owner && r.Name == repo.Name {
			found = true
			continue
		}
		updated = append(updated, r)
	}
	if !found {
		return fmt.Errorf("repo %s/%s not found", repo.Owner, repo.Name)
	}
	a.cfg.Settings.SkillRepos = updated
	// 保存配置
	if err := config.Save(a.cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	// 清理 skills 市场缓存，确保下次搜索不再包含已移除的仓库
	if a.marketStore != nil {
		n, err := a.marketStore.ClearAllCache()
		log.Printf("RemoveSkillRepo: cleared %d cache files, err=%v", n, err)
	}
	return nil
}

// UpdateSkillRepo 修改一个已配置的 GitHub 仓库扫描条目
// original 用于定位原条目(按 Owner+Name 匹配),updated 为新值(整体替换)
func (a *App) UpdateSkillRepo(original, updated config.SkillRepo) error {
	if err := a.assertInit(); err != nil {
		return err
	}
	if original.Owner == "" || original.Name == "" {
		return fmt.Errorf("original repo owner and name required")
	}
	if updated.Owner == "" || updated.Name == "" {
		return fmt.Errorf("updated repo owner and name required")
	}
	if updated.Branch == "" {
		updated.Branch = "main"
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg == nil {
		return fmt.Errorf("config not loaded")
	}

	// 1. 定位原条目
	origIdx := -1
	for i, r := range a.cfg.Settings.SkillRepos {
		if r.Owner == original.Owner && r.Name == original.Name {
			origIdx = i
			break
		}
	}
	if origIdx == -1 {
		return fmt.Errorf("repo %s/%s not found", original.Owner, original.Name)
	}

	// 2. 若 owner/name 发生变化,检查与其他条目冲突
	if !(updated.Owner == original.Owner && updated.Name == original.Name) {
		for i, r := range a.cfg.Settings.SkillRepos {
			if i == origIdx {
				continue
			}
			if r.Owner == updated.Owner && r.Name == updated.Name {
				return fmt.Errorf("repo %s/%s already exists", updated.Owner, updated.Name)
			}
		}
	}

	// 3. 原地替换
	a.cfg.Settings.SkillRepos[origIdx] = updated

	// 4. 保存配置
	if err := config.Save(a.cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	// 5. 清理 skills 市场缓存,确保下次搜索扫描到新仓库
	if a.marketStore != nil {
		n, err := a.marketStore.ClearAllCache()
		log.Printf("UpdateSkillRepo: cleared %d cache files, err=%v", n, err)
	}
	return nil
}

// CheckSkillUpdates 检查已安装 skills 的远程更新（手动触发）
func (a *App) CheckSkillUpdates() ([]skills.UpdateStatus, error) {
	if err := a.assertInit(); err != nil {
		return nil, err
	}
	if err := a.beginInFlight(); err != nil {
		return nil, err
	}
	defer a.endInFlight()

	_, _, _, ss, _, _ := a.snapshot()
	if ss == nil {
		return nil, fmt.Errorf("skills store not initialized")
	}
	return ss.CheckUpdates(a.registry), nil
}

func (a *App) OpenConfigFolder() error {
	dir := config.AgentPackDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	switch runtime.GOOS {
	case "windows":
		return exec.Command("explorer.exe", dir).Start()
	case "darwin":
		return exec.Command("open", dir).Start()
	default:
		return exec.Command("xdg-open", dir).Start()
	}
}

func (a *App) GetStartupErrors() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]string{}, a.startupErrors...)
}

func (a *App) GetSettings() (config.Settings, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.cfg == nil {
		return config.DefaultSettings(), nil
	}
	return a.cfg.Settings, nil
}

func (a *App) UpdateSettings(s config.Settings) error {
	if err := a.assertInit(); err != nil {
		return err
	}
	if err := a.beginInFlight(); err != nil {
		return err
	}
	defer a.endInFlight()

	// 获取 storeOpMu 以序列化与 RescanAgents、ToggleAgent 等存储操作的并发
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()

	// 收集锁内数据，I/O 操作在锁外执行
	var oldSkillStorage, oldSkillSyncMethod string
	var skillsStore *skills.Store
	var registry *agents.Registry
	var backups *backup.Manager

	a.mu.Lock()
	if a.cfg == nil {
		a.cfg = config.Default()
	}
	if s.BackupRetention <= 0 {
		if s.BackupCount > 0 {
			s.BackupRetention = s.BackupCount
		} else {
			s.BackupRetention = config.DefaultSettings().BackupRetention
		}
	}
	if s.BackupCount <= 0 {
		s.BackupCount = s.BackupRetention
	}
	newCfg := *a.cfg
	newCfg.Settings = s
	newSettings := newCfg.Settings
	oldSkillStorage = a.cfg.Settings.SkillStorage
	oldSkillSyncMethod = a.cfg.Settings.SkillSyncMethod
	skillsStore = a.skillsStore
	registry = a.registry
	backups = a.backups
	a.mu.Unlock()

	if err := config.Save(&newCfg); err != nil {
		return err
	}

	a.mu.Lock()
	a.cfg.Settings = newSettings
	a.refreshBackupHooksLocked()
	a.mu.Unlock()

	// 在释放锁之后执行文件 I/O 操作
	if skillsStore != nil {
		if s.SkillStorage != oldSkillStorage {
			newDir := skills.ResolveSSOTDir(skills.StorageLocation(s.SkillStorage))
			if result, err := skillsStore.MigrateStorage(newDir, registry); err != nil {
				log.Printf("migrate skill storage: %v", err)
			} else if result.Migrated > 0 {
				log.Printf("migrated %d skills to %s", result.Migrated, newDir)
			}
		}
		if s.SkillSyncMethod != oldSkillSyncMethod {
			skillsStore.SetSyncMethod(skills.SyncMethod(s.SkillSyncMethod))
			if err := skillsStore.Resync(registry); err != nil {
				log.Printf("resync skills after method change: %v", err)
			}
		}
	}
	if backups != nil {
		if err := backups.SetRetention(s.BackupRetention); err != nil {
			log.Printf("set backup retention: %v", err)
		}
	}

	// 在锁外 emit 事件
	if skillsStore != nil && (s.SkillStorage != oldSkillStorage || s.SkillSyncMethod != oldSkillSyncMethod) {
		a.emit("skills:changed", skillsStore.List())
	}

	return nil
}

// Quit 退出应用程序。设置 allowClose 标志后调用 wruntime.Quit。
func (a *App) Quit() error {
	a.mu.Lock()
	a.allowClose = true
	ctx := a.ctx
	closed := a.closed
	a.mu.Unlock()
	if closed || ctx == nil {
		return nil
	}
	wruntime.Quit(ctx)
	return nil
}

// HideWindow 隐藏窗口（最小化到系统托盘）。
func (a *App) HideWindow() {
	a.mu.RLock()
	ctx := a.ctx
	closed := a.closed
	a.mu.RUnlock()
	if closed || ctx == nil {
		return
	}
	wruntime.WindowHide(ctx)
}

// ShowWindow 显示窗口（从系统托盘恢复）。
func (a *App) ShowWindow() {
	a.mu.RLock()
	ctx := a.ctx
	closed := a.closed
	a.mu.RUnlock()
	if closed || ctx == nil {
		return
	}
	wruntime.WindowShow(ctx)
}

func (a *App) SetTheme(theme string) {
	a.mu.RLock()
	ctx := a.ctx
	closed := a.closed
	a.mu.RUnlock()
	if closed || ctx == nil {
		return
	}

	switch theme {
	case "dark":
		wruntime.WindowSetDarkTheme(ctx)
	case "light":
		wruntime.WindowSetLightTheme(ctx)
	default:
		wruntime.WindowSetSystemDefaultTheme(ctx)
	}
}

func (a *App) PickDirectory() (string, error) {
	a.mu.RLock()
	ctx := a.ctx
	closed := a.closed
	a.mu.RUnlock()
	if closed || ctx == nil {
		return "", fmt.Errorf("app not ready")
	}
	return wruntime.OpenDirectoryDialog(ctx, wruntime.OpenDialogOptions{
		Title: "选择目录",
	})
}

func (a *App) PickFile(filters string) (string, error) {
	a.mu.RLock()
	ctx := a.ctx
	closed := a.closed
	a.mu.RUnlock()
	if closed || ctx == nil {
		return "", fmt.Errorf("app not ready")
	}
	opts := wruntime.OpenDialogOptions{
		Title: "选择文件",
	}
	if filters != "" {
		for _, f := range strings.Split(filters, ",") {
			f = strings.TrimSpace(f)
			if f != "" {
				opts.Filters = append(opts.Filters, wruntime.FileFilter{
					DisplayName: f,
					Pattern:     f,
				})
			}
		}
	}
	return wruntime.OpenFileDialog(ctx, opts)
}

func (a *App) ListBackups() ([]backup.Summary, error) {
	a.mu.RLock()
	closed := a.closed
	backups := a.backups
	a.mu.RUnlock()
	if closed {
		return nil, fmt.Errorf("app is shutting down")
	}
	if backups == nil {
		return []backup.Summary{}, nil
	}
	return backups.ListSummaries()
}

func (a *App) GetBackup(id string) (backup.Snapshot, error) {
	a.mu.RLock()
	closed := a.closed
	backups := a.backups
	a.mu.RUnlock()
	if closed {
		return backup.Snapshot{}, fmt.Errorf("app is shutting down")
	}
	if backups == nil {
		return backup.Snapshot{}, fmt.Errorf("backup manager not initialized")
	}
	return backups.GetSnapshot(id)
}

func (a *App) DeleteBackup(id string) error {
	a.mu.RLock()
	closed := a.closed
	backups := a.backups
	a.mu.RUnlock()
	if closed {
		return fmt.Errorf("app is shutting down")
	}
	if backups == nil {
		return fmt.Errorf("backup manager not initialized")
	}
	return backups.Delete(id)
}

func (a *App) RestoreBackup(id string, opts backup.ImportOptions) (backup.ImportResult, error) {
	if err := a.assertInit(); err != nil {
		return backup.ImportResult{}, err
	}
	if err := a.beginInFlight(); err != nil {
		return backup.ImportResult{}, err
	}
	defer a.endInFlight()
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()

	var closed bool
	var exporter *backup.Exporter
	var backupsMgr *backup.Manager
	var mcpErr error

	func() {
		a.mu.RLock()
		closed = a.closed
		if !closed && opts.ApplyMCP {
			mcpErr = a.requireMcpStoreReadyLocked()
		}
		exporter = a.exporter
		backupsMgr = a.backups
		a.mu.RUnlock()
	}()

	if closed {
		return backup.ImportResult{}, fmt.Errorf("app is shutting down")
	}
	if mcpErr != nil {
		return backup.ImportResult{}, mcpErr
	}
	if exporter == nil {
		return backup.ImportResult{}, fmt.Errorf("exporter not initialized")
	}
	if backupsMgr == nil {
		return backup.ImportResult{}, fmt.Errorf("backup manager not initialized")
	}

	res, err := exporter.RestoreFromBackup(backupsMgr, id, opts)
	if err != nil {
		return res, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return res, nil
	}
	a.emitAgentsChangedLocked()
	a.emitLocked("mcp:changed", a.mcpStore.List())
	return res, nil
}

func (a *App) ExportBackupToFile(id, dest string) (string, error) {
	a.mu.RLock()
	closed := a.closed
	backups := a.backups
	a.mu.RUnlock()
	if closed {
		return "", fmt.Errorf("app is shutting down")
	}
	if backups == nil {
		return "", fmt.Errorf("backup manager not initialized")
	}
	return backups.ExportToFile(id, dest)
}

func (a *App) ImportBackupFromFile(src string, opts backup.ImportOptions) (backup.ImportResult, error) {
	if err := a.assertInit(); err != nil {
		return backup.ImportResult{}, err
	}
	if err := a.beginInFlight(); err != nil {
		return backup.ImportResult{}, err
	}
	defer a.endInFlight()
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()

	var closed bool
	var exporter *backup.Exporter
	var mcpErr error

	func() {
		a.mu.RLock()
		closed = a.closed
		if !closed && opts.ApplyMCP {
			mcpErr = a.requireMcpStoreReadyLocked()
		}
		exporter = a.exporter
		a.mu.RUnlock()
	}()

	if closed {
		return backup.ImportResult{}, fmt.Errorf("app is shutting down")
	}
	if mcpErr != nil {
		return backup.ImportResult{}, mcpErr
	}
	if exporter == nil {
		return backup.ImportResult{}, fmt.Errorf("exporter not initialized")
	}

	res, err := exporter.ImportFromFile(src, opts)
	if err != nil {
		return res, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return res, nil
	}

	// 应用导入的设置（直接保存，不走 UpdateSettings 以避免 storeOpMu 死锁）
	if opts.ApplySettings && len(res.ExportedSettings) > 0 {
		if data, err := json.Marshal(res.ExportedSettings); err == nil {
			var s config.Settings
			if err := json.Unmarshal(data, &s); err == nil {
				if s.BackupRetention <= 0 {
					if s.BackupCount > 0 {
						s.BackupRetention = s.BackupCount
					} else {
						s.BackupRetention = config.DefaultSettings().BackupRetention
					}
				}
				if s.BackupCount <= 0 {
					s.BackupCount = s.BackupRetention
				}
				if a.cfg == nil {
					a.cfg = config.Default()
				}
				a.cfg.Settings = s
				if err := config.Save(a.cfg); err != nil {
					log.Printf("import: save settings: %v", err)
				} else {
					a.refreshBackupHooksLocked()
					if a.backups != nil {
						if err := a.backups.SetRetention(s.BackupRetention); err != nil {
							log.Printf("import: set retention: %v", err)
						}
					}
				}
			}
		}
	}

	a.emitAgentsChangedLocked()
	a.emitLocked("mcp:changed", a.mcpStore.List())
	if opts.ApplySettings {
		a.emitLocked("settings:changed", a.cfg.Settings)
	}
	return res, nil
}

func (a *App) CreateBackupNow(description string) (backup.Summary, error) {
	a.mu.RLock()
	closed := a.closed
	backups := a.backups
	a.mu.RUnlock()
	if closed {
		return backup.Summary{}, fmt.Errorf("app is shutting down")
	}
	if backups == nil {
		return backup.Summary{}, fmt.Errorf("backup manager not initialized")
	}
	return backups.Capture("manual", "", "", description)
}

func (a *App) ListSkills() ([]skills.Skill, error) {
	_, _, _, ss, _, _ := a.snapshot()
	if ss == nil {
		return []skills.Skill{}, nil
	}
	return ss.List(), nil
}

func (a *App) ListSkillCapableAgents() ([]*agents.Agent, error) {
	reg, _, _, _, _, _ := a.snapshot()
	if reg == nil {
		return []*agents.Agent{}, nil
	}
	ids := reg.SkillCapableAgentIDs()
	out := make([]*agents.Agent, 0, len(ids))
	for _, id := range ids {
		if ag := reg.Get(id); ag != nil {
			out = append(out, ag)
		}
	}
	return out, nil
}

// AutoAdoptSkills 扫描 agent skill 目录，将未管理 skill 自动纳管到 SSOT。
func (a *App) AutoAdoptSkills() (skills.AdoptionResult, error) {
	if err := a.assertInit(); err != nil {
		return skills.AdoptionResult{}, err
	}
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return skills.AdoptionResult{}, fmt.Errorf("app is shutting down")
	}
	if a.skillsStore == nil {
		return skills.AdoptionResult{}, fmt.Errorf("skills store not initialized")
	}
	result := a.skillsStore.AutoAdopt(a.registry)
	if len(result.Adopted) > 0 || len(result.Conflicts) > 0 {
		a.emitLocked("skills:changed", a.skillsStore.List())
	}
	return result, nil
}

func (a *App) ImportSkillDirectory(path string, agentIDs []string) (skills.Skill, error) {
	if err := a.assertInit(); err != nil {
		return skills.Skill{}, err
	}
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return skills.Skill{}, fmt.Errorf("app is shutting down")
	}
	if a.skillsStore == nil {
		return skills.Skill{}, fmt.Errorf("skills store not initialized")
	}
	sk, err := a.skillsStore.Import(path, agentIDs, a.registry, "", "")
	if err != nil {
		return skills.Skill{}, err
	}
	a.emitLocked("skills:changed", a.skillsStore.List())
	return sk, nil
}

// InstallSkillFromZip 从 zip 文件安装 skill。
// 解压后自动识别含 SKILL.md 的根目录并纳管到 SSOT，同步到指定 agent 目录。
func (a *App) InstallSkillFromZip(zipPath string, agentIDs []string) (skills.Skill, error) {
	if err := a.assertInit(); err != nil {
		return skills.Skill{}, err
	}
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return skills.Skill{}, fmt.Errorf("app is shutting down")
	}
	if a.skillsStore == nil {
		return skills.Skill{}, fmt.Errorf("skills store not initialized")
	}
	sk, err := a.skillsStore.InstallFromZip(zipPath, agentIDs, a.registry)
	if err != nil {
		return skills.Skill{}, err
	}
	a.emitLocked("skills:changed", a.skillsStore.List())
	return sk, nil
}

func (a *App) ToggleSkillAgent(id, agentID string, enabled bool) error {
	if err := a.assertInit(); err != nil {
		return err
	}
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("app is shutting down")
	}
	if a.skillsStore == nil {
		return fmt.Errorf("skills store not initialized")
	}
	if err := a.skillsStore.ToggleAgent(id, agentID, enabled, a.registry); err != nil {
		return err
	}
	a.emitLocked("skills:changed", a.skillsStore.List())
	return nil
}

func (a *App) UninstallSkill(id string) (skills.UninstallResult, error) {
	if err := a.assertInit(); err != nil {
		return skills.UninstallResult{}, err
	}
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return skills.UninstallResult{}, fmt.Errorf("app is shutting down")
	}
	if a.skillsStore == nil {
		return skills.UninstallResult{}, fmt.Errorf("skills store not initialized")
	}
	result, err := a.skillsStore.Uninstall(id, a.registry)
	if err != nil {
		return skills.UninstallResult{}, err
	}
	a.emitLocked("skills:changed", a.skillsStore.List())
	return result, nil
}

func (a *App) ResyncSkills() error {
	if err := a.assertInit(); err != nil {
		return err
	}
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("app is shutting down")
	}
	if a.skillsStore == nil {
		return fmt.Errorf("skills store not initialized")
	}
	return a.skillsStore.Resync(a.registry)
}

// ScanUnmanagedSkills returns skills found in agent directories that are not
// managed by AgentPack (not present in the SSOT directory). Read-only operation.
func (a *App) ScanUnmanagedSkills() ([]skills.UnmanagedSkill, error) {
	if err := a.assertInit(); err != nil {
		return nil, err
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		return nil, fmt.Errorf("app is shutting down")
	}
	if a.skillsStore == nil {
		return nil, fmt.Errorf("skills store not initialized")
	}
	return a.skillsStore.ScanUnmanaged(a.registry), nil
}

func (a *App) MigrateSkillStorage(target string) (skills.MigrationResult, error) {
	if err := a.assertInit(); err != nil {
		return skills.MigrationResult{}, err
	}
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return skills.MigrationResult{}, fmt.Errorf("app is shutting down")
	}
	if a.skillsStore == nil {
		return skills.MigrationResult{}, fmt.Errorf("skills store not initialized")
	}
	newDir := skills.ResolveSSOTDir(skills.StorageLocation(target))
	result, err := a.skillsStore.MigrateStorage(newDir, a.registry)
	if err != nil {
		return skills.MigrationResult{}, err
	}
	a.emitLocked("skills:changed", a.skillsStore.List())
	return result, nil
}
