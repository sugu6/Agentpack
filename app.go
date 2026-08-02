package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"agentpack/internal/agents"
	"agentpack/internal/backup"
	"agentpack/internal/config"
	"agentpack/internal/database"
	"agentpack/internal/market"
	"agentpack/internal/mcp"
	"agentpack/internal/skills"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type App struct {
	ctx       context.Context
	wailsApp  *application.App
	mu        sync.RWMutex // 保护 App 内部状态（registry, stores, cfg）
	rescanMu  sync.Mutex   // 序列化 RescanAgents（先于 storeOpMu 获取）
	storeOpMu sync.Mutex   // 序列化 MCP/Skills 存储操作（后于 rescanMu）
	// ⚠️ 锁定顺序约定（违反将导致死锁）：
	//   1. rescanMu (仅在 RescanAgents 中获取)
	//   2. storeOpMu
	//   3. a.mu
	// Go vet 建议：新增方法若需要多种锁，请严格遵循此顺序。
	cfg                *config.AppConfig
	registry           *agents.Registry
	mcpStore           *mcp.Store
	mcpStoreReady      bool
	mcpStoreErr        string
	skillsStore        *skills.Store
	marketStore        *market.Store
	backups            *backup.Manager
	exporter           *backup.Exporter
	closed             bool
	allowClose         bool
	startupErrors      []string
	inFlight           int
	flightCond         *sync.Cond
	downloadCtx        context.Context
	downloadCancel     context.CancelFunc
	downloadPausedFile string        // 暂停时保存的临时文件路径
	downloadOffset     int64         // 暂停时的下载偏移量
	downloadURL        string        // 当前下载的 URL
	downloadedFile     string        // 下载完成的安装包路径（供 InstallUpdate 使用）
	downloadState      DownloadState // 下载状态（受 mu 保护）
	paused             int32         // 暂停标志（原子操作，0=否，1=是）
	tray               *application.SystemTray
	liteMu             sync.Mutex // 保护 liteMode / liteTimer，禁止在持有时获取 a.mu
	liteMode           bool
	liteTimer          *time.Timer
	liteUnit           time.Duration // 计时单位，生产为 time.Minute，测试可覆盖
}

// DownloadState 下载状态
type DownloadState int

const (
	DownloadStateIdle        DownloadState = iota // 空闲
	DownloadStateDownloading                      // 下载中
	DownloadStatePaused                           // 已暂停
	DownloadStateCompleted                        // 完成
	DownloadStateError                            // 错误
)

// setWailsApp 注入 v3 应用实例引用
func (a *App) setWailsApp(app *application.App) {
	a.wailsApp = app
}

// setTray 注入 v3 原生系统托盘引用
func (a *App) setTray(tray *application.SystemTray) {
	a.tray = tray
}

func NewApp(cfg *config.AppConfig) *App {
	a := &App{cfg: cfg}
	a.flightCond = sync.NewCond(&a.mu)
	a.downloadState = DownloadStateIdle
	return a
}

func (a *App) ServiceStartup(ctx context.Context, options application.ServiceOptions) error {
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

	// v3: Theme is set at window creation time via WindowsWindow.Theme.
	// Runtime theme switching is not available in v3 alpha.
	// TODO: When v3 stabilizes, implement runtime theme switching.

	a.registry = agents.NewRegistry()
	a.registry.Scan()
	a.registry.LoadDisabled(a.cfg.DisabledAgents)

	a.mcpStore = mcp.NewStore()
	if err := a.mcpStore.Load(a.registry); err != nil {
		addErr("mcp store load", err)
		// 部分配置损坏时 store 仍加载了可用数据（可读），但阻断写操作，
		// 避免基于不完整状态覆盖 agent 配置；错误详情经 requireMcpStoreReadyLocked 暴露。
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

	// 注册 MCP Server fetcher
	a.marketStore.RegisterServer(market.NewRegistryFetcher())

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
	// ServiceStartup 持有 a.mu，restartLiteTimer 内部需要 a.mu.RLock，
	// 因此异步启动首个计时器以避免自死锁
	go a.restartLiteTimer()
	return nil
}

func (a *App) ServiceShutdown() error {
	// 停止轻量模式空闲计时器，防止 ServiceShutdown 完成后 timer 回调触发
	a.stopLiteTimer()
	// v3: 系统托盘由 application.App 统一管理生命周期，无需手动清理
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
	return nil
}

// beforeClose 在 v3 中通过 RegisterHook 同步拦截窗口关闭事件。
// 调用 e.Cancel() 可阻止窗口关闭（由前端决定是隐藏还是退出）。
func (a *App) beforeClose(e *application.WindowEvent) {
	a.mu.RLock()
	closed := a.closed
	inFlight := a.inFlight
	allowClose := a.allowClose
	a.mu.RUnlock()

	if closed || allowClose {
		// 允许关闭，不取消事件
		return
	}
	if inFlight > 0 {
		a.emit("app:close-blocked")
		e.Cancel()
		return
	}
	// 发出关闭请求事件，前端根据配置决定是隐藏还是退出
	a.emit("app:close-requested")
	e.Cancel()
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
	a.wailsApp.Event.Emit(event, data...)
}

func (a *App) emitLocked(event string, data ...interface{}) {
	if a.closed || a.ctx == nil {
		return
	}
	a.wailsApp.Event.Emit(event, data...)
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

func (a *App) PauseDownload() error {
	a.mu.Lock()
	if a.closed || a.downloadState != DownloadStateDownloading {
		a.mu.Unlock()
		return fmt.Errorf("no active download")
	}
	a.mu.Unlock()
	atomic.StoreInt32(&a.paused, 1)
	return nil
}

// ResumeDownload 恢复已暂停的下载
func (a *App) ResumeDownload() error {
	a.mu.Lock()
	if a.closed || a.downloadState != DownloadStatePaused {
		a.mu.Unlock()
		return fmt.Errorf("no paused download to resume")
	}
	url := a.downloadURL
	offset := a.downloadOffset
	if url == "" || offset <= 0 {
		a.mu.Unlock()
		return fmt.Errorf("no saved download position")
	}
	a.downloadState = DownloadStateDownloading
	a.downloadPausedFile = ""
	a.mu.Unlock()

	// 必须在释放 a.mu 之后调用，startDownload 内部会再次加锁
	atomic.StoreInt32(&a.paused, 0)
	return a.startDownload(url, offset)
}

// GetDownloadState 获取当前下载状态（供前端查询）
func (a *App) GetDownloadState() (state string, fileName string, offset int64) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	switch a.downloadState {
	case DownloadStateIdle:
		state = "idle"
	case DownloadStateDownloading:
		state = "downloading"
	case DownloadStatePaused:
		state = "paused"
	case DownloadStateCompleted:
		state = "complete"
	case DownloadStateError:
		state = "error"
	}
	return state, a.downloadPausedFile, a.downloadOffset
}
