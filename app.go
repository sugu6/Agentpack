package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agentpack/internal/agents"
	"agentpack/internal/backup"
	"agentpack/internal/config"
	"agentpack/internal/crypto"
	"agentpack/internal/database"
	"agentpack/internal/i18n"
	"agentpack/internal/market"
	"agentpack/internal/mcp"
	"agentpack/internal/skills"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type App struct {
	ctx       context.Context
	wailsApp  *application.App
	mainWin   *application.WebviewWindow // 主窗口引用（main.go 创建后注入）
	mu        sync.RWMutex               // 保护 App 内部状态（registry, stores, cfg）
	rescanMu  sync.Mutex                 // 序列化 RescanAgents（先于 storeOpMu 获取）
	storeOpMu sync.Mutex                 // 序列化 MCP/Skills 存储操作（后于 rescanMu）
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
	startupErrors []string
	// 最近一次自动来源回填的结果（受 mu 保护；lastBackfillDone 区分"从未执行"与"结果为空"）
	lastBackfill       SkillSourceBackfillResult
	lastBackfillDone   bool
	inFlight           int
	flightCond         *sync.Cond
	downloadCtx        context.Context
	downloadCancel     context.CancelFunc
	downloadDone       chan struct{} // 当前下载 goroutine 结束信号（受 mu 保护），CancelDownload 等待其关闭
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
	// CheckUpdate 的 singleflight + 结果缓存状态（checkUpdateMu 保护）。
	// 缓存用于避免高频次反复请求 GitHub API（未认证 60 次/小时/IP）触发限流。
	checkUpdateMu      sync.Mutex
	checkUpdateCh      chan checkUpdateRes
	updateCheckAt      time.Time              // 上次检查时间
	updateCheckRateLtd bool                   // 上次结果是否命中限流(决定退避时长)
	updateCheckResult  *UpdateCheckResult     // 缓存的结果
	updateCheckMsgKey  string                 // 结果消息的 i18n key（cache 命中时按当前语言重生成）
	updateCheckMsgArgs map[string]interface{} // 消息参数
	updateCheckErr     error                  // 缓存的结果错误
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

// downloadStateNames 与 DownloadState 状态顺序一一对应。
var downloadStateNames = [...]string{"idle", "downloading", "paused", "complete", "error"}

// setWailsApp 注入 v3 应用实例引用
func (a *App) setWailsApp(app *application.App) {
	a.wailsApp = app
}

// setMainWindow 注入主窗口引用。HideWindow/showWindowRaw 必须使用该引用
// 而非 wailsApp.Window.Current()：v3 的 currentWindowID 只在窗口收到
// WM_ACTIVATE(WA_ACTIVE) 时赋值，主窗口从未被激活时 Current() 返回 nil，
// 直接 nil 解引用 panic（lite 空闲计时器到点隐藏窗口可触发）。
func (a *App) setMainWindow(win *application.WebviewWindow) {
	a.mu.Lock()
	a.mainWin = win
	a.mu.Unlock()
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

	// 加密密钥文件损坏时，后续备份导出/导入的敏感值加密全部不可用，
	// 必须作为启动错误展示，否则用户只会看到泛化的导出失败。
	// 恢复指引：删除损坏的 .machine_key.corrupt.<ts> 与新密钥文件后重启即可重新生成。
	if kerr := crypto.MachineKeyError(); kerr != nil {
		addErr("machine key", kerr)
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
		// Store 采用"部分加载"设计：损坏的配置被跳过，其余正常配置仍可管理。
		// 所有写操作均先读后写（读失败即拒绝写），失败配置不会被覆盖，
		// 因此不锁死整个模块；仅当数据库同步失败（内存状态已回滚）时才禁止操作。
		a.mcpStoreReady = a.mcpStore.Ready()
		a.mcpStoreErr = ""
		if !a.mcpStoreReady {
			a.mcpStoreErr = err.Error()
		}
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

	// 启动时清理过期的市场缓存：缓存键含 query+cursor+page，每次新查询
	// 写一个新文件，长期使用只增不减；过期条目读取时只跳过不删除。
	if removed, err := a.marketStore.CleanCache(); err != nil {
		log.Printf("market cache cleanup: %v", err)
	} else if removed > 0 {
		log.Printf("market cache cleanup: removed %d expired file(s)", removed)
	}

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
	// 启动后后台自动回填缺少仓库来源的技能（网络操作，不阻塞启动）
	go a.autoBackfillSources()
	return nil
}

func (a *App) ServiceShutdown() error {
	// 停止轻量模式空闲计时器，防止 ServiceShutdown 完成后 timer 回调触发
	a.stopLiteTimer()
	// v3: 系统托盘由 application.App 统一管理生命周期，无需手动清理
	a.mu.Lock()
	a.closed = true
	// 先取消活动下载再等待 inFlight：下载 goroutine 的 ctx 上限是 30 分钟，
	// 若不取消，inFlight 等待 5 秒超时后进程强杀 goroutine，removeTmp 清理
	// 不执行，Downloads 目录永久残留 .downloading 文件（下载完成才改名，
	// 下次更新 URL 变更时 os.Create 覆盖也命中不了，无法回收）。
	var dlDone chan struct{}
	if a.downloadCancel != nil {
		a.downloadCancel()
		dlDone = a.downloadDone
	}
	if a.inFlight > 0 {
		// 后台 goroutine 在超时后强制 Broadcast，避免 Wait() 在任务挂起时永久阻塞
		// close(done) 必须在 Unlock() 之前调用，确保主循环重新获取 a.mu 时 done 已关闭，
		// 否则主循环可能命中 select 的 default 分支并再次 Wait()，而 goroutine 已退出不再 Broadcast
		done := make(chan struct{})
		var closeOnce sync.Once
		go func() {
			select {
			case <-time.After(5 * time.Second):
				a.mu.Lock()
				a.flightCond.Broadcast()
				closeOnce.Do(func() { close(done) })
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
		// for 循环正常退出（inFlight 归零）：主动关闭 done 通知超时 goroutine 退出，
		// 否则 <-done 会阻塞至 5 秒超时。使用 sync.Once 避免与超时分支双重 close。
		closeOnce.Do(func() { close(done) })
	waitDone:
		a.mu.Unlock()
		<-done
	} else {
		a.mu.Unlock()
	}

	// 等待下载 goroutine 完成清理（removeTmp）。取消后应快速退出；
	// 极端卡死时 2 秒兜底，不阻塞退出流程。
	if dlDone != nil {
		select {
		case <-dlDone:
		case <-time.After(2 * time.Second):
			log.Printf("shutdown: download goroutine did not exit within 2s, tmp file may remain")
		}
	}

	if a.backups != nil {
		done := make(chan struct{})
		go func() {
			// Shutdown 先置 closed 拒绝新备份再 Wait，防止关闭期间 MCP 变更
			// 触发的 runAsync Add 与 Wait 并发导致 "WaitGroup misuse" panic。
			a.backups.Shutdown()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			log.Printf("shutdown: timeout waiting for backup hooks")
		}
	}

	// 排空无 inFlight 保护的 store 写操作（AddMcpServer/ToggleAgent/
	// withSkillsStore 系列等只持 storeOpMu）：它们在 closed 检查后、
	// 写库前可能仍在途。database.Close 与它们的事务并发会让最后一次
	// 写静默失败（回滚保证一致，但用户操作丢失）。取一次 storeOpMu
	// 等队列排空，之后不会再有新操作进入（closed 已置位）。
	a.storeOpMu.Lock()
	a.storeOpMu.Unlock()

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
		// 允许关闭，不取消事件。窗口随后被销毁：置空 mainWin 引用，
		// 防止退出流程中（closed 尚未置位）的 ShowWindow 回调命中
		// wails 的"窗口已销毁则静默重建"逻辑，闪现一个正在退出的窗口。
		a.mu.Lock()
		a.mainWin = nil
		a.mu.Unlock()
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
	return a.beginInFlightLocked()
}

// beginInFlightLocked 在已持有 a.mu 的临界区内登记 in-flight 计数。
// startDownload 等需要在持锁状态下完成"状态转换 + 登记"的原子操作，
// 避免解锁后、登记前被 CancelDownload 介入造成状态撕裂。
func (a *App) beginInFlightLocked() error {
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

// isClosed 返回应用是否已进入关闭流程。
func (a *App) isClosed() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.closed
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

// withStoreOp acquires storeOpMu then mu in order, checks closed, runs fn.
func (a *App) withStoreOp(fn func() error) error {
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
	return fn()
}

// withStoreOpMcp wraps withStoreOp with an additional mcp store ready check.
func (a *App) withStoreOpMcp(fn func() error) error {
	return a.withStoreOp(func() error {
		if err := a.requireMcpStoreReadyLocked(); err != nil {
			return err
		}
		return fn()
	})
}

// withSkillsStore wraps withStoreOp with a nil-checked skills store.
func (a *App) withSkillsStore(fn func(*skills.Store) error) error {
	return a.withStoreOp(func() error {
		if a.skillsStore == nil {
			return fmt.Errorf("skills store not initialized")
		}
		return fn(a.skillsStore)
	})
}

// withBackups checks closed/nil and invokes fn with the backup manager.
func withBackups[T any](a *App, fn func(*backup.Manager) (T, error)) (T, error) {
	a.mu.RLock()
	closed, backups := a.closed, a.backups
	a.mu.RUnlock()
	if closed {
		var zero T
		return zero, fmt.Errorf("app is shutting down")
	}
	if backups == nil {
		var zero T
		return zero, fmt.Errorf("backup manager not initialized")
	}
	return fn(backups)
}

// prepareRestore 校验关闭状态、MCP store 就绪状态与 exporter/backups 初始化状态，
// 供 RestoreBackup / ImportBackupFromFile 复用。
func (a *App) prepareRestore(opts backup.ImportOptions) (*backup.Exporter, *backup.Manager, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		return nil, nil, fmt.Errorf("app is shutting down")
	}
	if opts.ApplyMCP {
		if err := a.requireMcpStoreReadyLocked(); err != nil {
			return nil, nil, err
		}
	}
	if a.exporter == nil {
		return nil, nil, fmt.Errorf("exporter not initialized")
	}
	if a.backups == nil {
		return nil, nil, fmt.Errorf("backup manager not initialized")
	}
	return a.exporter, a.backups, nil
}

// typed snapshot getters - eliminates 5+ ignored underscore values per call.
func (a *App) getMcp() *mcp.Store            { _, ms, _, _, _, _ := a.snapshot(); return ms }
func (a *App) getMarket() *market.Store      { _, _, mks, _, _, _ := a.snapshot(); return mks }
func (a *App) getSkills() *skills.Store      { _, _, _, ss, _, _ := a.snapshot(); return ss }
func (a *App) getRegistry() *agents.Registry { reg, _, _, _, _, _ := a.snapshot(); return reg }
func (a *App) getBackups() *backup.Manager   { _, _, _, _, bm, _ := a.snapshot(); return bm }

// emitMcpChangedLocked emits agents:changed + mcp:changed (a.mu already held).
// Safe when mcpStore may be nil.
func (a *App) emitMcpChangedLocked() {
	a.emitAgentsChangedLocked()
	if a.mcpStore != nil {
		a.emitLocked("mcp:changed", a.mcpStore.List())
	}
}

// withConfigLocked acquires storeOpMu then mu, validates cfg, runs fn, and saves config.
func (a *App) withConfigLocked(fn func() error) error {
	if err := a.assertInit(); err != nil {
		return err
	}
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg == nil {
		return fmt.Errorf("config not loaded")
	}
	if err := fn(); err != nil {
		return err
	}
	return a.saveConfigAndClearMarketCache()
}

// saveConfigAndClearMarketCache persists config and clears the market cache.
func (a *App) saveConfigAndClearMarketCache() error {
	if err := config.Save(a.cfg); err != nil {
		return err
	}
	if a.marketStore != nil {
		n, _ := a.marketStore.ClearAllCache()
		log.Printf("saveConfigAndClearMarketCache: cleared %d cache files", n)
	}
	return nil
}

// rebuildTrayIfNeeded rebuilds the tray menu when language changes.
func (a *App) rebuildTrayIfNeeded(oldLang, newLang string) {
	if oldLang == newLang || a.tray == nil {
		return
	}
	application.InvokeAsync(func() { rebuildTrayMenu(a.tray, newLang) })
}

// syncLiteModeIfNeeded restarts/stops the idle timer when lite config changes.
func (a *App) syncLiteModeIfNeeded(oldEnabled, newEnabled bool) {
	if oldEnabled == newEnabled {
		return
	}
	if newEnabled {
		a.restartLiteTimer()
	} else {
		a.stopLiteTimer()
	}
}
func (a *App) ListAgents() ([]*agents.Agent, error) {
	// 单次快照同时取 registry 与 mcp store，避免两次 getX() 之间
	// RescanAgents 原子换代导致 reg/ms 来自不同代（MCP 计数错乱）。
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
		// 部分配置损坏时 store 仍可用（只跳过损坏配置），不中断整个重扫；
		// 仅当数据库同步失败（状态已回滚）时才中止。
		if !newMcpStore.Ready() {
			return nil, fmt.Errorf("mcp reload: %w", err)
		}
		log.Printf("mcp reload (partial): %v", err)
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
	a.refreshBackupHooksLocked()
	a.emitLocked("agents:changed", all)
	a.emitLocked("mcp:changed", a.mcpStore.List())
	a.emitLocked("skills:changed", a.skillsStore.List())
	a.mu.Unlock()

	// 重新绑定备份管理器/导出器（取 m.mu）。必须放在 a.mu 临界区之外：
	// 备份 Capture 持 m.mu 时会回调 cfgProvider 取 a.mu.RLock，若此处持 a.mu 再取
	// m.mu 则构成 a.mu→m.mu 与 m.mu→a.mu 的反向锁序死锁。
	a.backups.Bind(newReg, newMcpStore)
	a.exporter = backup.NewExporter(newMcpStore, newReg)
	a.setConfigProviders()

	return all, nil
}

func (a *App) GetAgent(id string) (*agents.Agent, error) {
	reg := a.getRegistry()
	if reg == nil {
		return nil, nil
	}
	return reg.Get(id), nil
}

func (a *App) ToggleAgent(id string, enabled bool) error {
	return a.withStoreOp(func() error {
		if a.registry == nil {
			return fmt.Errorf("registry not initialized")
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
	})
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
	ms := a.getMcp()
	if ms == nil {
		return []mcp.Server{}, nil
	}
	return ms.List(), nil
}

func (a *App) ScanMcpServers() (*mcp.ScanResult, error) {
	// 单次 snapshot：getRegistry/getMcp 是两次独立 RLock，之间 RescanAgents
	// 可完成整代替换，旧 reg 的 ConfigPath × 新 ms 的 managedKeys 会把已管理
	// 条目误标为"未管理"（ListAgents 已用同一模式规避）
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
	ms := a.getMcp()
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
	ms := a.getMcp()
	if ms == nil {
		return []mcp.Server{}, nil
	}
	return ms.ByAgent(agentID), nil
}

func (a *App) AddMcpServer(server mcp.Server, agentIDs []string) error {
	return a.withStoreOpMcp(func() error {
		if _, err := a.mcpStore.Add(server, agentIDs, a.registry); err != nil {
			return err
		}
		a.emitMcpChangedLocked()
		return nil
	})
}

func (a *App) UpdateMcpServer(id string, server mcp.Server, agentIDs []string) error {
	return a.withStoreOpMcp(func() error {
		if err := a.mcpStore.Update(id, server, agentIDs, a.registry); err != nil {
			return err
		}
		a.emitMcpChangedLocked()
		return nil
	})
}

func (a *App) DeleteMcpServer(id string) error {
	return a.withStoreOpMcp(func() error {
		if err := a.mcpStore.Remove(id, a.registry); err != nil {
			return err
		}
		a.emitMcpChangedLocked()
		return nil
	})
}

func (a *App) ToggleMcpServerAgent(id, agentID string, enabled bool) error {
	return a.withStoreOpMcp(func() error {
		if err := a.mcpStore.ToggleAgent(id, agentID, enabled, a.registry); err != nil {
			return err
		}
		a.emitMcpChangedLocked()
		return nil
	})
}

func (a *App) SearchMarketServers(source, query, cursor string, pageSize int) (*market.SearchResultServers, error) {
	mks := a.getMarket()
	if mks == nil {
		return nil, fmt.Errorf("market store not initialized")
	}
	ctx, cancel := market.ContextWithTimeout(120 * time.Second)
	defer cancel()
	return mks.Search(ctx, market.Source(source), market.SearchOptions{
		Query:    query,
		PageSize: pageSize,
		Cursor:   cursor,
	})
}

func (a *App) GetMarketServer(source, sourceID string) (market.MarketServer, error) {
	mks := a.getMarket()
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
	var created mcp.Server
	err := a.withStoreOpMcp(func() error {
		var err error
		created, err = a.mcpStore.Add(mcp.Server{
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
		return err
	})
	if err != nil {
		if errors.Is(err, mcp.ErrDuplicateServer) {
			// %w 保留 "duplicate server:" 稳定前缀，前端据此判定"已安装跳过"，
			// 而不会误吞"同名但 key 不同"的真实冲突错误。
			return mcp.Server{}, fmt.Errorf("%w (use edit to change its agents)", mcp.ErrDuplicateServer)
		}
		return mcp.Server{}, err
	}
	a.mu.Lock()
	a.emitMcpChangedLocked()
	a.mu.Unlock()
	return created, nil
}

// SearchMarketSkills 搜索市场中的 Skills，合并所有来源并按下载量排序。
// source 参数："" 表示搜索全部启用的来源，"github" 仅 GitHub 仓库，"skills-sh" 仅 skills.sh
// page 从 1 开始，支持分页（无限滚动）
func (a *App) SearchMarketSkills(query string, pageSize int, page int, source string) (*market.SearchResultSkills, error) {
	mks := a.getMarket()
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
	// 所有可用来源均被禁用时直接返回空结果，而不是让 store 的
	// "nil = 搜索全部已注册来源" 语义绕过禁用设置（nil 与空切片无法区分）。
	if len(enabledSources) == 0 {
		return &market.SearchResultSkills{Items: []market.MarketSkill{}, Total: 0, Page: 1}, nil
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

	reg, ss := a.getRegistry(), a.getSkills()
	if ss == nil || reg == nil {
		return skills.Skill{}, fmt.Errorf("skills store or registry not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// 分支兜底枚举：{存储分支} ∪ {main, master} 去重。
	// 场景：扫描侧通过 jsDelivr @master 别名（=仓库默认分支）解析出的分支
	// 存为 "master"，而真实默认分支可能是 main（别名只用于扫描，不保证
	// 与仓库实际分支名一致）；反之 master-only 仓库在默认 main 404 时需重试。
	// 依次尝试直到成功，成功后以实际命中的分支落库。
	attempts := []string{branch, "main", "master"}
	seen := map[string]bool{}
	var installed skills.Skill
	var lastErr error
	for _, cand := range attempts {
		if cand == "" || seen[cand] {
			continue
		}
		seen[cand] = true
		tarballURL := fmt.Sprintf("https://codeload.github.com/%s/%s/tar.gz/refs/heads/%s",
			skill.RepoOwner, skill.RepoName, cand)
		input := skills.TarballInstallInput{
			TarballURL: tarballURL,
			Directory:  skill.Directory,
			FullPath:   skill.FullPath, // 传递完整相对路径（如 "skills/pdf"），安装时精准定位
			RepoOwner:  skill.RepoOwner,
			RepoName:   skill.RepoName,
			RepoBranch: cand,
		}
		// 5 分钟总预算共享给多个候选分支时，第一个失败的分支可能耗尽整个
		// 预算，后续候选直接因 ctx 超时失败。为每个候选派生独立子预算
		// （90 秒/候选，3 候选共 270 秒，不超过 5 分钟总预算）。
		childCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		installed, lastErr = ss.InstallFromTarball(childCtx, input, agentIDs, reg)
		cancel()
		if lastErr == nil {
			branch = cand
			skill.RepoBranch = cand
			break
		}
	}
	if lastErr != nil {
		return skills.Skill{}, lastErr
	}

	// 写入 ~/.agents/.skill-lock.json（兼容 CC Switch 等工具）
	lockEntry := skills.AgentsLockEntry{
		Directory:  skill.Directory,
		Source:     skill.RepoOwner + "/" + skill.RepoName,
		SourceType: "github",
		SourceURL:  "https://github.com/" + skill.RepoOwner + "/" + skill.RepoName,
		SkillPath:  filepath.Join(ss.SSOTDir(), skill.Directory),
		Branch:     branch,
		FullPath:   skill.FullPath,
	}
	if err := skills.WriteAgentsLock(lockEntry); err != nil {
		// 锁文件写入失败不阻断安装，仅记录日志
		log.Printf("warning: write agents lock for %s: %v", skill.Directory, err)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.emitAgentsChangedLocked()
	a.emitLocked("skills:changed", ss.List())
	// 安装成功后异步缓存 Commit SHA 作为更新检测基线
	go func() {
		_ = skills.CacheSkillCommitSHA(installed.ID, skill.RepoOwner, skill.RepoName,
			skill.RepoBranch)
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
	return a.withConfigLocked(func() error {
		if repo.Owner == "" || repo.Name == "" {
			return fmt.Errorf("repo owner and name required")
		}
		if repo.Branch == "" {
			repo.Branch = "main"
		}
		for _, r := range a.cfg.Settings.SkillRepos {
			if r.Owner == repo.Owner && r.Name == repo.Name {
				return fmt.Errorf("repo %s/%s already exists", repo.Owner, repo.Name)
			}
		}
		a.cfg.Settings.SkillRepos = append(a.cfg.Settings.SkillRepos, repo)
		return nil
	})
}

// RemoveSkillRepo 从扫描列表移除一个 GitHub 仓库
func (a *App) RemoveSkillRepo(repo config.SkillRepo) error {
	return a.withConfigLocked(func() error {
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
		return nil
	})
}

// UpdateSkillRepo 修改一个已配置的 GitHub 仓库扫描条目
// original 用于定位原条目(按 Owner+Name 匹配),updated 为新值(整体替换)
func (a *App) UpdateSkillRepo(original, updated config.SkillRepo) error {
	return a.withConfigLocked(func() error {
		if original.Owner == "" || original.Name == "" {
			return fmt.Errorf("original repo owner and name required")
		}
		if updated.Owner == "" || updated.Name == "" {
			return fmt.Errorf("updated repo owner and name required")
		}
		if updated.Branch == "" {
			updated.Branch = "main"
		}
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
		a.cfg.Settings.SkillRepos[origIdx] = updated
		return nil
	})
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
	// 与 UpdateSkill/UpdateSkills 互斥：CheckUpdates 会读 SSOT 目录
	// (localDirFileHashes/readUpdateCache) 并可能 RemoveAgentsLockEntry，
	// 与 UpdateSkill 的 tarball fallback（RemovePath + 重建 SSOT）并发时
	// 会读到半写入目录、误报"有更新"，且 lock 条目的写删互相竞争。
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()

	ss := a.getSkills()
	if ss == nil {
		return nil, fmt.Errorf("skills store not initialized")
	}
	return ss.CheckUpdates(a.registry), nil
}

// UpdateSkill updates a single skill to the latest remote version
func (a *App) UpdateSkill(skillID string) (skills.Skill, error) {
	if err := a.assertInit(); err != nil {
		return skills.Skill{}, err
	}
	if err := a.beginInFlight(); err != nil {
		return skills.Skill{}, err
	}
	defer a.endInFlight()
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()

	ss := a.getSkills()
	if ss == nil {
		return skills.Skill{}, fmt.Errorf("skills store not initialized")
	}
	updated, err := ss.UpdateSkill(skillID, a.registry)
	if err != nil {
		return skills.Skill{}, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.emitAgentsChangedLocked()
	a.emitLocked("skills:changed", ss.List())
	return updated, nil
}

// UpdateSkills batch-updates multiple skills
func (a *App) UpdateSkills(skillIDs []string) (skills.UpdateSkillsResult, error) {
	if err := a.assertInit(); err != nil {
		return skills.UpdateSkillsResult{}, err
	}
	if err := a.beginInFlight(); err != nil {
		return skills.UpdateSkillsResult{}, err
	}
	defer a.endInFlight()
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()

	ss := a.getSkills()
	if ss == nil {
		return skills.UpdateSkillsResult{}, fmt.Errorf("skills store not initialized")
	}
	result := ss.UpdateSkills(skillIDs, a.registry)

	a.mu.Lock()
	defer a.mu.Unlock()
	a.emitAgentsChangedLocked()
	a.emitLocked("skills:changed", ss.List())
	return result, nil
}

// openSystem 用系统默认程序打开路径或 URL。
func openSystem(path string) error {
	switch runtime.GOOS {
	case "windows":
		cmd := exec.Command("explorer.exe", path)
		hideConsoleWindow(cmd)
		return cmd.Start()
	case "darwin":
		return exec.Command("open", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}

func (a *App) OpenConfigFolder() error {
	dir := config.AgentPackDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return openSystem(dir)
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

	res, err := a.applySettingsLocked(s)
	if err != nil {
		return err
	}
	newLang := i18n.ResolveLanguage(s.Language)
	a.rebuildTrayIfNeeded(res.oldLang, newLang)
	a.syncLiteModeIfNeeded(res.oldLiteEnabled, s.LiteAutoEnabled)
	a.emit("settings:changed", res.newSettings)
	return nil
}

// settingsApplyResult holds data collected during applySettingsLocked.
type settingsApplyResult struct {
	newSettings    config.Settings
	oldLang        string
	oldLiteEnabled bool
}

// extractSettingsFromSnapshot 从快照 settings.appSettings 解码应用设置，
// 供导入/恢复在应用 MCP 之前预检（settings 数据非法时提前失败，避免
// MCP 已落盘后的部分生效状态）。
func extractSettingsFromSnapshot(snap backup.Snapshot) (*config.Settings, error) {
	if snap.Settings == nil {
		return nil, nil
	}
	raw, ok := snap.Settings["appSettings"]
	if !ok {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, nil
	}
	data, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("restore: encode settings: %w", err)
	}
	var settings config.Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("restore: decode settings: %w", err)
	}
	settings = normalizeBackupConfig(settings)
	return &settings, nil
}

// normalizeBackupConfig 归一化备份保留配置：BackupRetention 与 BackupCount 互为兜底。
// BackupRetention 为 0 表示无限保留（不清理旧快照），只有负数才回退兜底。
func normalizeBackupConfig(s config.Settings) config.Settings {
	if s.BackupRetention < 0 {
		if s.BackupCount > 0 {
			s.BackupRetention = s.BackupCount
		} else {
			s.BackupRetention = config.DefaultSettings().BackupRetention
		}
	}
	if s.BackupCount <= 0 {
		s.BackupCount = s.BackupRetention
	}
	return s
}

// applySettingsLocked applies settings changes while holding a.mu.
// It collects old values before modifying state, performs file I/O outside the lock,
// and returns the data needed for post-lock side effects (tray rebuild, lite timer).
// 返回错误时，内存中设置已回滚为旧值，调用方应将其透传给前端。
func (a *App) applySettingsLocked(s config.Settings) (settingsApplyResult, error) {
	// Normalize backup config
	s = normalizeBackupConfig(s)
	s.LiteAutoDelay = config.ClampLiteDelay(s.LiteAutoDelay)

	var oldSkillStorage, oldSkillSyncMethod string
	var skillsStore *skills.Store
	var registry *agents.Registry
	var backups *backup.Manager
	var oldLang string
	var oldLiteEnabled bool

	a.mu.Lock()
	if a.cfg == nil {
		a.cfg = config.Default()
	}
	oldLiteEnabled = a.cfg.Settings.LiteAutoEnabled
	oldSkillStorage = a.cfg.Settings.SkillStorage
	oldSkillSyncMethod = a.cfg.Settings.SkillSyncMethod
	oldLang = i18n.ResolveLanguage(a.cfg.Settings.Language)
	skillsStore = a.skillsStore
	registry = a.registry
	backups = a.backups
	oldSettings := a.cfg.Settings
	// skillRepos 有专门的 Add/Remove API，UpdateSettings 的全量替换会抹掉
	// 并发修改：用户添加 repo 后，在途 autoSave 携带旧 skillRepos 快照
	// 到达后端即把新增仓库静默回滚（前端 display 合并只保护 UI，不保护
	// 已发出的 payload）。以现存配置为准，仓库列表的变更走专用 API。
	s.SkillRepos = oldSettings.SkillRepos
	newCfg := *a.cfg
	newCfg.Settings = s
	newSettings := newCfg.Settings
	a.cfg.Settings = newSettings
	a.refreshBackupHooksLocked()
	a.mu.Unlock()

	result := settingsApplyResult{newSettings: newSettings, oldLang: oldLang, oldLiteEnabled: oldLiteEnabled}
	// 失败路径统一回滚内存设置，避免"前端收到失败但设置已部分生效"的不一致
	rollbackAll := func() {
		a.mu.Lock()
		if a.cfg != nil {
			a.cfg.Settings = oldSettings
			// 恢复设置后必须同步恢复备份 hook：成功路径在写 Settings 后立即
			// refreshBackupHooksLocked 切换 hook；若此处只回滚设置而 hook 保持
			// 新的 nil/非 nil 值（如 AutoBackup 从 true→false 保存失败），
			// 后续 MCP 变更将不再触发自动备份，用户以为在备份而实际已静默失效。
			a.refreshBackupHooksLocked()
		}
		a.mu.Unlock()
	}

	// rollbackSyncMethod reverts the skill sync method in both the store and in-memory cfg.
	rollbackSyncMethod := func() {
		skillsStore.SetSyncMethod(skills.SyncMethod(oldSkillSyncMethod))
		a.mu.Lock()
		if a.cfg != nil {
			a.cfg.Settings.SkillSyncMethod = oldSkillSyncMethod
		}
		a.mu.Unlock()
	}

	// File I/O outside the lock
	// 以下对 skillsStore 的修改（SetSyncMethod/Resync/MigrateStorage）虽在 a.mu 释放后执行，
	// 但 skills.Store 内部以 s.mu（sync.RWMutex）保护所有读写操作，因此与 ListSkills 的 RLock 读不竞争。
	if skillsStore != nil {
		if s.SkillSyncMethod != oldSkillSyncMethod {
			skillsStore.SetSyncMethod(skills.SyncMethod(s.SkillSyncMethod))
			if err := skillsStore.Resync(registry); err != nil {
				// Resync 失败后若只 rollbackAll（恢复 cfg.Settings），
				// store 内仍以新 sync method 运行，后续行为与配置不一致：
				// 必须先恢复 store 的 method 再回滚配置
				rollbackSyncMethod()
				rollbackAll()
				return result, fmt.Errorf("apply settings: resync skills: %w", err)
			}
		}
		if s.SkillStorage != oldSkillStorage {
			newDir := skills.ResolveSSOTDir(skills.StorageLocation(s.SkillStorage))
			migrated, err := skillsStore.MigrateStorage(newDir, registry)
			if err != nil {
				// MigrateStorage 失败时目录与指针已内部回滚，恢复 store 的
				// sync method（若本次也改了）与内存配置
				if s.SkillSyncMethod != oldSkillSyncMethod {
					rollbackSyncMethod()
				}
				rollbackAll()
				return result, fmt.Errorf("apply settings: migrate skill storage: %w", err)
			}
			if migrated.Migrated > 0 {
				log.Printf("migrated %d skills to %s", migrated.Migrated, newDir)
			}
		}
	}

	if err := config.Save(&newCfg); err != nil {
		if skillsStore != nil && s.SkillStorage != oldSkillStorage {
			oldDir := skills.ResolveSSOTDir(skills.StorageLocation(oldSkillStorage))
			if _, rollbackErr := skillsStore.MigrateStorage(oldDir, registry); rollbackErr != nil {
				log.Printf("rollback skill storage after settings save failure: %v", rollbackErr)
			}
		}
		if skillsStore != nil && s.SkillSyncMethod != oldSkillSyncMethod {
			rollbackSyncMethod()
			if rollbackErr := skillsStore.Resync(registry); rollbackErr != nil {
				log.Printf("rollback skill sync method after settings save failure: %v", rollbackErr)
			}
		}
		rollbackAll()
		return result, fmt.Errorf("apply settings: save config: %w", err)
	}

	// Emit skills:changed if storage or sync method changed
	if skillsStore != nil && (s.SkillStorage != oldSkillStorage || s.SkillSyncMethod != oldSkillSyncMethod) {
		a.emit("skills:changed", skillsStore.List())
	}

	if backups != nil {
		if err := backups.SetRetention(s.BackupRetention); err != nil {
			log.Printf("set backup retention: %v", err)
		}
	}

	return result, nil
}

// OpenURL 在系统浏览器中打开指定 URL。
func (a *App) OpenURL(url string) {
	if !isSafeURL(url) {
		log.Printf("OpenURL: blocked unsafe URL scheme: %q", url)
		return
	}
	if err := openSystem(url); err != nil {
		log.Printf("OpenURL: %v", err)
	}
}

// isSafeURL 校验 URL 是否允许 http/https scheme，防止恶意 scheme 被系统打开器执行。
// 与前端 api.ts 的 openUrl 白名单（^https?:\/\/）等价：不仅要求 scheme 为 http/https，
// 还要求带 host（url.Parse 下 "https:foo" 这类无 host 形式会被放行，前端则直接拒绝）。
func isSafeURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u.Host != ""
	}
	return false
}

// Quit 退出应用程序。设置 allowClose 标志后调用 application.Quit。
// 有任务在途（下载/更新/迁移/备份）时拒绝退出并通知前端——
// 与 beforeClose 的 inFlight 拦截策略保持一致；否则退出流程 5 秒
// 超时后强杀任务 goroutine，下载残留 .downloading 文件、备份半写。
func (a *App) Quit() error {
	a.mu.Lock()
	inFlight := a.inFlight
	closed := a.closed
	a.mu.Unlock()
	if closed {
		return nil
	}
	if inFlight > 0 {
		a.emit("app:close-blocked")
		return fmt.Errorf("tasks in progress, close blocked")
	}
	a.mu.Lock()
	a.allowClose = true
	a.mu.Unlock()
	a.wailsApp.Quit()
	return nil
}

// HideWindow 隐藏窗口（最小化到系统托盘）。
func (a *App) HideWindow() {
	if a.isClosed() {
		return
	}
	// mainWin 由 setMainWindow/beforeClose 持 a.mu 写入，读点必须同步取
	// 副本：否则与 beforeClose 置 nil 并发时可能读到已销毁窗口的旧指针，
	// 命中 wails 的"窗口已销毁则静默重建"，在退出流程中闪现窗口。
	win := a.mainWindow()
	if win != nil {
		win.Hide()
		return
	}
	// 防御：窗口引用未注入时回退到 Current()（极端启动时序下可能为 nil）
	if w := a.wailsApp.Window.Current(); w != nil {
		w.Hide()
	}
}

// ShowWindow 显示窗口（从系统托盘恢复）。同时退出轻量模式并停用空闲计时器，
// 计时器由前端上报的用户活动（NotifyActivity）重新拉起。
func (a *App) ShowWindow() {
	a.liteMu.Lock()
	wasLite := a.liteMode
	a.liteMode = false
	a.stopLiteTimerLocked()
	a.liteMu.Unlock()

	a.showWindowRaw()

	if wasLite && onLiteModeChanged != nil {
		onLiteModeChanged(false)
	}
}

// showWindowRaw 仅执行窗口显示，不触碰轻量模式状态。
func (a *App) showWindowRaw() {
	if a.isClosed() {
		return
	}
	win := a.mainWindow()
	if win != nil {
		win.Show()
		return
	}
	if w := a.wailsApp.Window.Current(); w != nil {
		w.Show()
	}
}

// mainWindow 持 a.mu 返回当前主窗口引用副本。所有读点都必须经过它，
// 与 setMainWindow/beforeClose 的持锁写入配对。
func (a *App) mainWindow() *application.WebviewWindow {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.mainWin
}

// getTheme 返回当前主题配置（供 winbridge 系统主题切换钩子读取）
func (a *App) getTheme() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.cfg == nil {
		return "system"
	}
	return a.cfg.Settings.Theme
}

func (a *App) SetTheme(theme string) {
	if a.isClosed() {
		return
	}

	// v3 alpha 无运行时 SetTheme 公共 API，通过 WndProcInterceptor 缓存的 HWND
	// 直接调用 DwmSetWindowAttribute(DWMWA_USE_IMMERSIVE_DARK_MODE) 实现。
	hwnd := getMainWindowHWND()
	if hwnd == 0 {
		return
	}
	switch theme {
	case "dark":
		SetDarkMode(hwnd, true)
	case "light":
		SetDarkMode(hwnd, false)
	case "system":
		SetDarkMode(hwnd, isDarkMode())
	}
}

func (a *App) PickDirectory() (string, error) {
	if a.isClosed() {
		return "", fmt.Errorf("app not ready")
	}
	return a.wailsApp.Dialog.OpenFile().
		SetTitle("选择目录").
		CanChooseFiles(false).
		CanChooseDirectories(true).
		PromptForSingleSelection()
}

func (a *App) PickFile(filters string) (string, error) {
	if a.isClosed() {
		return "", fmt.Errorf("app not ready")
	}
	dialog := a.wailsApp.Dialog.OpenFile().
		SetTitle("选择文件")
	if filters != "" {
		for _, f := range strings.Split(filters, ",") {
			f = strings.TrimSpace(f)
			if f != "" {
				dialog = dialog.AddFilter(f, f)
			}
		}
	}
	return dialog.PromptForSingleSelection()
}

func (a *App) ListBackups() ([]backup.Summary, error) {
	return withBackups(a, func(m *backup.Manager) ([]backup.Summary, error) {
		return m.ListSummaries()
	})
}

func (a *App) GetBackup(id string) (backup.Snapshot, error) {
	return withBackups(a, func(m *backup.Manager) (backup.Snapshot, error) {
		return m.GetSnapshot(id)
	})
}

func (a *App) DeleteBackup(id string) error {
	_, err := withBackups(a, func(m *backup.Manager) (any, error) {
		return nil, m.Delete(id)
	})
	return err
}

func (a *App) RestoreBackup(id string, opts backup.ImportOptions) (backup.ImportResult, error) {
	if err := a.assertInit(); err != nil {
		return backup.ImportResult{}, err
	}
	if err := a.beginInFlight(); err != nil {
		return backup.ImportResult{}, err
	}
	defer a.endInFlight()

	// 持锁阶段（store 的 MCP 恢复 + Agent 状态提取）收敛到闭包内，
	// 锁的释放始终由 defer 保证。此前实现在此处手动 Unlock → 调用
	// UpdateSettings → 重新 Lock：若 UpdateSettings 链路 panic，
	// 函数退出时 deferred Unlock 会作用在已解锁的 Mutex 上，
	// 触发 "sync: unlock of unlocked mutex" 并掩盖原始 panic。
	res, importedSettings, cfgAfter, err := a.restoreBackupLocked(id, opts)
	if err != nil {
		return res, err
	}
	if a.isClosed() {
		return res, nil
	}

	// storeOpMu 已释放，UpdateSettings 可安全获取同一把锁
	if importedSettings != nil {
		if settingsErr := a.UpdateSettings(*importedSettings); settingsErr != nil {
			return res, fmt.Errorf("restore: apply settings: %w", settingsErr)
		}
	} else if opts.ApplyAgentStatus {
		if err := config.Save(&cfgAfter); err != nil {
			return res, fmt.Errorf("restore: save agent status: %w", err)
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return res, nil
	}
	a.emitMcpChangedLocked()
	return res, nil
}

// restoreBackupLocked 执行 RestoreBackup 的持锁阶段，返回恢复结果、
// 待应用的导入设置与含最新 Agent 禁用列表的配置快照。
func (a *App) restoreBackupLocked(id string, opts backup.ImportOptions) (backup.ImportResult, *config.Settings, config.AppConfig, error) {
	var noCfg config.AppConfig
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()

	exporter, backupsMgr, err := a.prepareRestore(opts)
	if err != nil {
		return backup.ImportResult{}, nil, noCfg, err
	}

	// 前置解码并校验备份内设置：在应用 MCP 之前完成。RestoreFromBackup
	// 成功后 MCP/绑定已全部落盘，若随后设置解码/应用失败，恢复是"部分
	// 生效"且无回滚——用户看到失败提示，系统却已改。settings 数据非法
	// 在这里就失败，MCP 尚未被触碰。
	var importedSettings *config.Settings
	if opts.ApplySettings {
		snap, gerr := backupsMgr.GetSnapshot(id)
		if gerr != nil {
			return backup.ImportResult{}, nil, noCfg, fmt.Errorf("restore: read backup: %w", gerr)
		}
		importedSettings, err = extractSettingsFromSnapshot(snap)
		if err != nil {
			return backup.ImportResult{}, nil, noCfg, err
		}
	}

	// 挂起自动备份 hook：RestoreFromBackup 内部逐条 Add/Update 会触发
	// OnMutation 逐个 Capture，几十个服务器的快照产出几十个"中间状态"
	// 快照，把用户历史手动快照挤出 retention 配额。
	suppress := a.backups.Suppress()
	// defer 释放：RestoreFromBackup panic 时计数也必须归还，否则自动
	// 备份永久静默失效（Suppress 计数再也无法归零）。
	defer suppress()
	res, err := exporter.RestoreFromBackup(backupsMgr, id, opts)
	if err != nil {
		return res, nil, noCfg, err
	}

	if opts.ApplySettings && res.ExportedSettings != nil && len(res.ExportedSettings) > 0 {
		// res.ExportedSettings 为 nil 表示未提取到设置（旧快照/未导出设置），
		// 不视为错误；仅当解码失败（类型错配）才报错——此时 MCP 已应用，
		// 错误信息明确说明"服务器已恢复"。
		data, marshalErr := json.Marshal(res.ExportedSettings)
		if marshalErr != nil {
			return res, nil, noCfg, fmt.Errorf("restore: encode settings: %w", marshalErr)
		}
		var settings config.Settings
		if unmarshalErr := json.Unmarshal(data, &settings); unmarshalErr != nil {
			return res, nil, noCfg, fmt.Errorf("restore: apply settings: %w", unmarshalErr)
		}
		settings = normalizeBackupConfig(settings)
		importedSettings = &settings
	}

	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return res, nil, noCfg, nil
	}
	if a.cfg == nil {
		a.cfg = config.Default()
	}
	// 恢复 Agent 状态后必须与 ImportBackupFromFile 一致地持久化禁用列表，
	// 否则重启后 registry.LoadDisabled 从旧 config 读取，恢复的状态丢失。
	if opts.ApplyAgentStatus && a.registry != nil {
		a.cfg.DisabledAgents = a.registry.DisabledIDs()
	}
	cfgAfter := *a.cfg
	a.mu.Unlock()

	return res, importedSettings, cfgAfter, nil
}

func (a *App) ExportBackupToFile(id, dest string) (string, error) {
	return withBackups(a, func(m *backup.Manager) (string, error) {
		return m.ExportToFile(id, dest)
	})
}

func (a *App) ImportBackupFromFile(src string, opts backup.ImportOptions) (backup.ImportResult, error) {
	if err := a.assertInit(); err != nil {
		return backup.ImportResult{}, err
	}
	if err := a.beginInFlight(); err != nil {
		return backup.ImportResult{}, err
	}
	defer a.endInFlight()

	// 持锁阶段收敛到闭包内，锁的释放始终由 defer 保证（见 RestoreBackup
	// 的说明：手动 Unlock/Lock 在 panic 时会触发未持锁解锁并掩盖根因）。
	res, importedSettings, cfgAfter, err := a.importBackupLocked(src, opts)
	if err != nil {
		return res, err
	}
	if a.isClosed() {
		return res, nil
	}

	// storeOpMu 已释放，UpdateSettings 可安全获取同一把锁
	if importedSettings != nil {
		if settingsErr := a.UpdateSettings(*importedSettings); settingsErr != nil {
			return res, fmt.Errorf("import: apply settings: %w", settingsErr)
		}
	} else if opts.ApplyAgentStatus {
		if err := config.Save(&cfgAfter); err != nil {
			return res, fmt.Errorf("import: save agent status: %w", err)
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.emitMcpChangedLocked()
	return res, nil
}

// importBackupLocked 执行 ImportBackupFromFile 的持锁阶段（见 restoreBackupLocked）。
func (a *App) importBackupLocked(src string, opts backup.ImportOptions) (backup.ImportResult, *config.Settings, config.AppConfig, error) {
	var noCfg config.AppConfig
	a.storeOpMu.Lock()
	defer a.storeOpMu.Unlock()

	exporter, _, err := a.prepareRestore(opts)
	if err != nil {
		return backup.ImportResult{}, nil, noCfg, err
	}

	// 前置解码并校验备份内设置（与 RestoreBackup 同理）：ImportFromFile
	// 应用 MCP 之后若设置解码失败，恢复是"部分生效"且无回滚。
	var importedSettings *config.Settings
	if opts.ApplySettings {
		info, serr := os.Stat(src)
		if serr != nil {
			return backup.ImportResult{}, nil, noCfg, fmt.Errorf("import: stat file: %w", serr)
		}
		const maxImportSize = 100 * 1024 * 1024
		if info.Size() > maxImportSize {
			return backup.ImportResult{}, nil, noCfg, fmt.Errorf("import file too large: %d bytes (max %d MB)", info.Size(), maxImportSize/(1024*1024))
		}
		data, rerr := os.ReadFile(src)
		if rerr != nil {
			return backup.ImportResult{}, nil, noCfg, fmt.Errorf("import: read file: %w", rerr)
		}
		snap, derr := backup.DecodeSnapshot(data)
		if derr != nil {
			return backup.ImportResult{}, nil, noCfg, derr
		}
		importedSettings, err = extractSettingsFromSnapshot(snap)
		if err != nil {
			return backup.ImportResult{}, nil, noCfg, err
		}
	}

	// 挂起自动备份 hook（见 RestoreBackup 注释）
	suppress := a.backups.Suppress()
	// defer 释放：ImportFromFile panic 时计数也必须归还（见 RestoreBackup）
	defer suppress()
	res, err := exporter.ImportFromFile(src, opts)
	if err != nil {
		return res, nil, noCfg, err
	}

	if opts.ApplySettings && res.ExportedSettings != nil && len(res.ExportedSettings) > 0 {
		// res.ExportedSettings 为 nil 表示未提取到设置（旧快照/未导出设置），
		// 不视为错误；仅当解码失败（类型错配）才报错——此时 MCP 已应用，
		// 错误信息明确说明"服务器已恢复"。
		data, marshalErr := json.Marshal(res.ExportedSettings)
		if marshalErr != nil {
			return res, nil, noCfg, fmt.Errorf("import: encode settings: %w", marshalErr)
		}
		var settings config.Settings
		if unmarshalErr := json.Unmarshal(data, &settings); unmarshalErr != nil {
			return res, nil, noCfg, fmt.Errorf("import: apply settings: %w", unmarshalErr)
		}
		settings = normalizeBackupConfig(settings)
		importedSettings = &settings
	}

	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return res, nil, noCfg, nil
	}
	if a.cfg == nil {
		a.cfg = config.Default()
	}
	if opts.ApplyAgentStatus && a.registry != nil {
		a.cfg.DisabledAgents = a.registry.DisabledIDs()
	}
	cfgAfterAgentStatus := *a.cfg
	a.mu.Unlock()

	return res, importedSettings, cfgAfterAgentStatus, nil
}

func (a *App) CreateBackupNow(description string) (backup.Summary, error) {
	// 参与 in-flight 计数：backup.Capture 不检查 m.closed 也不加入 wg，
	// 若不加 beginInFlight，关闭流程的 wg.Wait 返回后 database.Close 可能
	// 与正在执行的事务并发，导致备份静默失败（"sql: database is closed"）。
	if err := a.beginInFlight(); err != nil {
		return backup.Summary{}, err
	}
	defer a.endInFlight()
	return withBackups(a, func(m *backup.Manager) (backup.Summary, error) {
		return m.Capture("manual", "", "", description)
	})
}

func (a *App) ListSkills() ([]skills.Skill, error) {
	ss := a.getSkills()
	if ss == nil {
		return []skills.Skill{}, nil
	}
	return ss.List(), nil
}

func (a *App) ListSkillCapableAgents() ([]*agents.Agent, error) {
	reg := a.getRegistry()
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
	var result skills.AdoptionResult
	err := a.withSkillsStore(func(ss *skills.Store) error {
		result = ss.AutoAdopt(a.registry)
		if len(result.Adopted) > 0 || len(result.Conflicts) > 0 {
			a.emitLocked("skills:changed", ss.List())
		}
		return nil
	})
	return result, err
}

func (a *App) ImportSkillDirectory(path string, agentIDs []string) (skills.Skill, error) {
	var sk skills.Skill
	err := a.withSkillsStore(func(ss *skills.Store) error {
		var e error
		sk, e = ss.Import(path, agentIDs, a.registry, "", "")
		if e != nil {
			return e
		}
		a.emitLocked("skills:changed", ss.List())
		return nil
	})
	return sk, err
}

// InstallSkillFromZip 从 zip 文件安装 skill。
// 解压后自动识别含 SKILL.md 的根目录并纳管到 SSOT，同步到指定 agent 目录。
func (a *App) InstallSkillFromZip(zipPath string, agentIDs []string) (skills.Skill, error) {
	var sk skills.Skill
	err := a.withSkillsStore(func(ss *skills.Store) error {
		var e error
		sk, e = ss.InstallFromZip(zipPath, agentIDs, a.registry)
		if e != nil {
			return e
		}
		a.emitLocked("skills:changed", ss.List())
		return nil
	})
	return sk, err
}

func (a *App) ToggleSkillAgent(id, agentID string, enabled bool) error {
	return a.withSkillsStore(func(ss *skills.Store) error {
		if err := ss.ToggleAgent(id, agentID, enabled, a.registry); err != nil {
			return err
		}
		a.emitLocked("skills:changed", ss.List())
		return nil
	})
}

func (a *App) UninstallSkill(id string) (skills.UninstallResult, error) {
	var result skills.UninstallResult
	err := a.withSkillsStore(func(ss *skills.Store) error {
		var e error
		result, e = ss.Uninstall(id, a.registry)
		if e != nil {
			return e
		}
		a.emitLocked("skills:changed", ss.List())
		return nil
	})
	return result, err
}

func (a *App) ResyncSkills() error {
	return a.withSkillsStore(func(ss *skills.Store) error {
		return ss.Resync(a.registry)
	})
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

// PauseDownload 暂停当前正在进行的下载
func (a *App) PauseDownload() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.downloadState != DownloadStateDownloading {
		return fmt.Errorf("no active download")
	}
	// 置位必须在持锁临界区内完成：下载 goroutine 在锁内做状态转换
	// (Downloading→Completed/Paused)。若解锁后再置位，窗口期内下载可能
	// 已完成，留下 paused=1 且 state=Completed 的错位——恢复时报
	// "no paused download"，文件实际已完整，UI 却显示失败。
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
	// 与 PauseDownload 对称：锁内清暂停标志，避免与暂停/取消的置位交错
	atomic.StoreInt32(&a.paused, 0)
	a.mu.Unlock()
	// 不在此处读取 url/offset：由 startDownload(resume=true) 在持锁时从
	// a.downloadURL/a.downloadOffset 读取并连同状态一起转换，消除与
	// CancelDownload 清空状态之间的检查-使用竞态。
	return a.startDownload("", 0, true)
}

// GetDownloadState 获取当前下载状态（供前端查询）
func (a *App) GetDownloadState() (state string, fileName string, offset int64) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if i := int(a.downloadState); i >= 0 && i < len(downloadStateNames) {
		state = downloadStateNames[i]
	}
	// downloadPausedFile 是完整临时路径（xxx.downloading），前端只展示
	// 文件名，剥掉目录部分避免把临时目录路径暴露给 UI。
	name := a.downloadPausedFile
	if name != "" {
		name = filepath.Base(name)
	}
	return state, name, a.downloadOffset
}

func (a *App) MigrateSkillStorage(target string) (skills.MigrationResult, error) {
	var result skills.MigrationResult
	err := a.withSkillsStore(func(ss *skills.Store) error {
		newDir := skills.ResolveSSOTDir(skills.StorageLocation(target))
		var e error
		result, e = ss.MigrateStorage(newDir, a.registry)
		if e != nil {
			return e
		}
		a.emitLocked("skills:changed", ss.List())
		return nil
	})
	return result, err
}

// SkillSourceBackfillResult 是从 skills.sh 回填技能来源的结果统计。
type SkillSourceBackfillResult struct {
	Matched    []string `json:"matched"`
	Mismatched []string `json:"mismatched"`
	Unmatched  []string `json:"unmatched"`
	Failed     []string `json:"failed"`
}

// BackfillSkillSources 从 skills.sh 回填缺少仓库来源的技能，使其支持后续更新。
// 仅处理 RepoOwner/RepoName 均为空的技能，已有来源的不覆盖。
// 写入前验证仓库中确实存在同名技能且远程 SKILL.md 与本地一致，
// 防止把不同来源/版本的技能错误关联到仓库。
func (a *App) BackfillSkillSources() (SkillSourceBackfillResult, error) {
	if err := a.assertInit(); err != nil {
		return SkillSourceBackfillResult{}, err
	}
	a.mu.RLock()
	closed := a.closed
	var directories []string
	var ss *skills.Store
	if a.skillsStore != nil {
		// 捕获 store 指针到局部变量，避免释放 RLock 后 RescanAgents 替换 a.skillsStore 导致悬空访问
		ss = a.skillsStore
		for _, sk := range ss.List() {
			if sk.RepoOwner == "" && sk.RepoName == "" {
				directories = append(directories, sk.Directory)
			}
		}
	}
	a.mu.RUnlock()
	if closed {
		return SkillSourceBackfillResult{}, fmt.Errorf("app is shutting down")
	}
	if len(directories) == 0 {
		return SkillSourceBackfillResult{}, nil
	}
	ssotDir := ss.SSOTDir()

	ctx, cancel := market.ContextWithTimeout(120 * time.Second)
	defer cancel()
	matches, err := market.BackfillSkillSources(ctx, directories)
	if err != nil {
		return SkillSourceBackfillResult{}, err
	}
	verify := func(dir string, cands []market.BackfillCandidate) (market.BackfillCandidate, string, string, bool, bool) {
		hadNetworkErr := false
		for _, c := range cands {
			fp, branch, ok, verr := verifyBackfillCandidate(ctx, ssotDir, dir, c)
			if verr != nil {
				hadNetworkErr = true
				continue
			}
			if ok {
				// 只接受远端目录名与本地一致的匹配（拒绝内容回退匹配的目录名不同场景），
				// 防止市场页上同仓库同名但内容不同的条目被误判为「已安装」。
				if !market.AcceptBackfillMatch(dir, fp) {
					continue
				}
				return c, fp, branch, true, false
			}
		}
		return market.BackfillCandidate{}, "", "", false, hadNetworkErr
	}
	return applyBackfillWithVerification(matches, directories, verify, ss.HasDirectory, a.applyBackfillEntry), nil
}

// applyBackfillEntry 在 storeOpMu 下用"当前" store 重新校验技能仍被纳管
// （回填验证期间用户可能已卸载，旧 store 指针的 HasDirectory 检查不反映
// RescanAgents 后的新状态），校验通过则同步内存来源。返回 false 时调用方
// 回滚刚写入的 lock 条目，避免锁文件残留已卸载技能的来源。
func (a *App) applyBackfillEntry(dir string, entry skills.AgentsLockEntry) bool {
	ok := false
	_ = a.withStoreOp(func() error {
		ss := a.skillsStore
		if ss == nil || !ss.HasDirectory(dir) {
			return nil
		}
		owner, repo, _ := strings.Cut(entry.Source, "/")
		if owner == "" || repo == "" {
			return nil
		}
		// SetRepoSource 以技能 ID（"skill:"+目录名）为 map 键，不能传裸目录名，
		// 否则恒返回 false，回填来源被回滚、功能失效。
		ok = ss.SetRepoSource("skill:"+dir, owner, repo, entry.Branch, entry.FullPath)
		return nil
	})
	return ok
}

// verifyBackfillCandidate 验证单个回填候选：先按 main 分支，查询失败时
// 追加一次 master 尝试（master 默认分支的仓库），与安装/更新侧的分支
// 兜底行为保持一致，避免 master-only 仓库的来源回填永久无果。
// 返回匹配成功时实际使用的分支（写入 lock，后续更新检测按此分支进行）。
func verifyBackfillCandidate(ctx context.Context, ssotDir, dir string, c market.BackfillCandidate) (fullPath, branch string, ok bool, err error) {
	localDir := filepath.Join(ssotDir, dir)
	fp, ok, err := skills.VerifySkillSource(ctx, dir, c.Owner, c.Repo, "main", localDir)
	if err == nil {
		return fp, "main", ok, nil
	}
	fp2, ok2, err2 := skills.VerifySkillSource(ctx, dir, c.Owner, c.Repo, "master", localDir)
	if err2 != nil {
		return fp, "", ok, err
	}
	return fp2, "master", ok2, nil
}

// backfillVerifier 验证候选列表：按序验证（下载量降序），返回首个内容一致的
// 匹配（含 fullPath 与实际匹配分支）。ok=false 且 networkErr=true 表示候选全部
// 因网络失败未验证；ok=false 且 networkErr=false 表示候选都验证过但内容不一致。
type backfillVerifier func(dir string, candidates []market.BackfillCandidate) (match market.BackfillCandidate, fullPath, branch string, ok bool, networkErr bool)

// applyBackfillWithVerification 并发验证匹配项并把通过验证的写入 lock。
// 拆分为独立函数便于单元测试（网络查询/验证由调用方注入）。
// stillExists 在写 lock 前校验技能仍被 store 纳管：回填验证期间（后台最多
// 120s 窗口）用户可能已卸载该技能，跳过写回避免锁文件残留已删除技能的来源。
// applyMemory 在写 lock 后同步内存来源（storeOpMu 下用当前 store 重新校验）：
// 返回 false 时回滚 lock 条目并静默跳过，防止"写 lock 后、落内存前"卸载
// 的窗口残留条目，同时保证 CheckUpdates/UpdateSkill 入口立即可用。
func applyBackfillWithVerification(matches map[string][]market.BackfillCandidate, directories []string, verify backfillVerifier, stillExists func(dir string) bool, applyMemory func(dir string, entry skills.AgentsLockEntry) bool) SkillSourceBackfillResult {
	type verified struct {
		dir        string
		match      market.BackfillCandidate
		fullPath   string
		branch     string
		ok         bool
		networkErr bool
	}
	verifiedMap := make(map[string]verified, len(matches))
	var mu sync.Mutex
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	for dir, cands := range matches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			m, fullPath, branch, ok, networkErr := verify(dir, cands)
			mu.Lock()
			verifiedMap[dir] = verified{dir: dir, match: m, fullPath: fullPath, branch: branch, ok: ok, networkErr: networkErr}
			mu.Unlock()
		}()
	}
	wg.Wait()

	var res SkillSourceBackfillResult
	for _, dir := range directories {
		cands, ok := matches[dir]
		if !ok || len(cands) == 0 {
			res.Unmatched = append(res.Unmatched, dir)
			continue
		}
		v := verifiedMap[dir]
		if v.networkErr && !v.ok {
			res.Failed = append(res.Failed, dir)
			continue
		}
		if !v.ok {
			res.Mismatched = append(res.Mismatched, dir)
			continue
		}
		if stillExists != nil && !stillExists(dir) {
			// 技能已被卸载：静默跳过（不写入锁文件，也不计入失败统计）
			log.Printf("backfill: skip %s: skill was uninstalled during verification", dir)
			continue
		}
		entry := skills.AgentsLockEntry{
			Directory:  dir,
			Source:     v.match.Owner + "/" + v.match.Repo,
			SourceType: "github",
			SourceURL:  "https://github.com/" + v.match.Owner + "/" + v.match.Repo,
			Branch:     v.branch,
			FullPath:   v.fullPath,
		}
		if err := skills.WriteAgentsLock(entry); err != nil {
			log.Printf("backfill source for %s: %v", dir, err)
			res.Failed = append(res.Failed, dir)
			continue
		}
		if applyMemory != nil && !applyMemory(dir, entry) {
			// 技能在"写 lock 后、落内存前"被卸载（或应用关闭）：回滚 lock
			// 条目并静默跳过，不把已卸载技能的来源留在锁文件。
			log.Printf("backfill: %s: skill gone or app closing after lock write, rolling back", dir)
			if rerr := skills.RemoveAgentsLockEntry(dir); rerr != nil {
				log.Printf("backfill: rollback lock entry for %s: %v", dir, rerr)
			}
			continue
		}
		res.Matched = append(res.Matched, dir)
	}
	return res
}

// autoBackfillSources 在启动后后台自动执行一次来源回填（替代设置页手动按钮）。
// 仅处理无仓库来源的技能；结果存入内存供前端查询，仅在匹配成功时
// emit skills:backfill 事件（前端只提示成功项，失败/未匹配静默并写入日志）。
func (a *App) autoBackfillSources() {
	res, err := a.BackfillSkillSources()
	a.mu.Lock()
	a.lastBackfill = res
	a.lastBackfillDone = true
	// emitLocked 读 a.closed/a.ctx，须在持锁状态下调用（与 emitMcpChangedLocked 一致）
	if err == nil && len(res.Matched) > 0 {
		a.emitLocked("skills:backfill", res)
	}
	a.mu.Unlock()
	if err != nil {
		log.Printf("auto backfill skill sources: %v", err)
		return
	}
	log.Printf("auto backfill skill sources: matched %d, mismatched %d, unmatched %d, failed %d",
		len(res.Matched), len(res.Mismatched), len(res.Unmatched), len(res.Failed))
}

// GetLastBackfillResult 返回最近一次自动来源回填的结果；从未执行过时 ok=false。
// 供前端启动挂载时兜底查询（事件可能因前端尚未就绪而丢失）。
func (a *App) GetLastBackfillResult() (SkillSourceBackfillResult, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.lastBackfillDone {
		return SkillSourceBackfillResult{}, false
	}
	return a.lastBackfill, true
}
