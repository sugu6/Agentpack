import {
  CreateBackupNow,
  DeleteBackup,
  ExportBackupToFile,
  GetAgent,
  GetAgentMcpServers,
  GetAppVersion,
  GetMarketServer,
  GetMcpServer,
  GetSettings,
  GetSkillRepos,
  GetStartupErrors,
  ImportBackupFromFile,
  ImportSkillDirectory,
  InstallMarketServer,
  InstallMarketSkill,
  InstallSkillFromZip,
  ListAgents,
  ListBackups,
  ListMcpServers,
  ListSkillCapableAgents,
  ListSkills,
  MigrateSkillStorage,
  OpenConfigFolder,
  PickDirectory,
  PickFile,
  RescanAgents,
  ResyncSkills,
  RestoreBackup,
  ScanMcpServers,
  ScanUnmanagedSkills,
  SearchMarketServers,
  SearchMarketSkills,
  ToggleAgent,
  ToggleMcpServerAgent,
  ToggleSkillAgent,
  UninstallSkill,
  AdoptSkillCopy,
  CleanOrphanSkillLinks,
  ConvertSkillCopyToLink,
  KeepSkillFork,
  OverwriteSkillCopyFromSSOT,
  ScanSkillConflicts,
  AddMcpServer,
  AddSkillRepo,
  CancelDownload,
  CheckSkillUpdates,
  CheckUpdate,
  DeleteMcpServer,
  GetLastBackfillResult,
  RemoveSkillRepo,
  StartDownloadUpdate,
  PauseDownload,
  ResumeDownload,
  InstallUpdate,
  UpdateSkillRepo,
  UpdateMcpServer,
  UpdateSettings,
  UpdateSkill,
  UpdateSkills,
  SetTheme,
  HideWindow,
  OpenURL,
  Quit,
  ShowWindow,
  NotifyActivity,
} from '../../bindings/agentpack/app'
import { Events } from '@wailsio/runtime'
import type * as AgentsNS from '../../bindings/agentpack/internal/agents/models'
import type * as ConfigNS from '../../bindings/agentpack/internal/config/models'
import type * as MarketNS from '../../bindings/agentpack/internal/market/models'
import type * as McpNS from '../../bindings/agentpack/internal/mcp/models'

export type Agent = AgentsNS.Agent
export interface Settings {
  theme: 'light' | 'dark' | 'system'
  marketSources: Record<string, { enabled: boolean; lastSync?: number }>
  autoBackup: boolean
  backupCount: number
  backupRetention: number
  skillStorage: 'agentpack' | 'unified'
  skillSyncMethod: 'symlink' | 'copy'
  skillRepos: SkillRepo[]
  windowAction: 'minimize' | 'exit'
  windowNoRemind: boolean
  language: string
  liteAutoEnabled: boolean
  liteAutoDelay: number
}

export interface SkillRepo {
  owner: string
  name: string
  branch: string
}

export interface UpdateCheckResult {
  hasUpdate: boolean
  currentVersion: string
  latestVersion: string
  message: string
  changelog: string
  releaseUrl: string
  downloadUrl: string
  downloadSize: number
  downloadName: string
}
type Theme = Settings['theme']
type WailsMcpServer = McpNS.Server
type WailsMarketServer = MarketNS.MarketServer

export class ApiError extends Error {
  type: 'network' | 'validation' | 'permission' | 'unknown'
  originalError?: Error
  
  constructor(message: string, type: ApiError['type'] = 'unknown', originalError?: Error) {
    super(message)
    this.name = 'ApiError'
    this.type = type
    this.originalError = originalError
  }
  
  static from(error: unknown): ApiError {
    if (error instanceof ApiError) {
      return error
    }
    if (error instanceof Error) {
      if (error.message.includes('network') || error.message.includes('connection')) {
        return new ApiError(error.message, 'network', error)
      }
      if (error.message.includes('permission') || error.message.includes('denied')) {
        return new ApiError(error.message, 'permission', error)
      }
      if (error.message.includes('valid')) {
        return new ApiError(error.message, 'validation', error)
      }
      return new ApiError(error.message, 'unknown', error)
    }
    return new ApiError(String(error), 'unknown')
  }
}

function optimizeToPlainObject<T>(obj: T): T {
  if (obj === null || obj === undefined) return obj
  if (typeof obj === 'string' || typeof obj === 'number' || typeof obj === 'boolean') return obj
  if (obj instanceof Date) return new Date(obj).toISOString() as unknown as T
  if (obj instanceof RegExp) return obj.toString() as unknown as T
  if (Array.isArray(obj)) return obj.map(item => optimizeToPlainObject(item)) as unknown as T
  if (typeof obj === 'object') {
    const plain: Record<string, unknown> = {}
    for (const [key, value] of Object.entries(obj as Record<string, unknown>)) {
      plain[key] = optimizeToPlainObject(value)
    }
    return plain as T
  }
  return obj
}

export interface McpServer {
  id: string
  name: string
  description?: string
  command: string
  args: string[]
  env?: Record<string, string>
  cwd?: string
  transport: string
  configType?: string
  url?: string
  headers?: Record<string, string>
  enabled?: boolean
  timeout?: number
  source: string
  sourceId?: string
  boundAgents: string[] | null
  installedAt: string
  updatedAt: string
}

export interface ScanSource {
  agentId: string
  agentName: string
  configPath: string
}

export interface ScanItem {
  server: McpServer
  managed: boolean
  agentId: string
  agentName: string
  configPath: string
  sources: ScanSource[]
}

export interface ScanResult {
  items: ScanItem[]
  total: number
  managed: number
  newFound: number
  failed: number
}

export type MarketSource = 'official' | 'github' | 'local' | (string & {})

export interface MarketServer {
  id: string
  name: string
  title?: string
  description: string
  homepage?: string
  docs?: string
  tags: string[]
  transport?: string
  command?: string
  args?: string[]
  env?: Record<string, string>
  url?: string
  source: MarketSource
  sourceId: string
  installs?: number
  stars?: number
  updatedAt: string
  /** Official 特有字段（仅 source='official' 时填充，用于前端筛选） */
  registry?: string
}

export interface SearchResultServers {
  items: MarketServer[]
  total: number
  page: number
  hasMore: boolean
  nextPage?: string
}

export type SkillMarketSource = 'github' | 'skills-sh' | (string & {})

export interface MarketSkill {
  id: string
  name: string
  description: string
  directory: string
  /** SKILL.md 所在目录的完整相对路径（如 "skills/pdf"，根目录为空）— 用于安装时精准定位 */
  fullPath?: string
  source: SkillMarketSource
  sourceId: string
  installs: number
  repoOwner: string
  repoName: string
  repoBranch: string
  readmeUrl?: string
  updatedAt: string
  contentHash?: string
}

export interface SourceStatus {
  source: string
  /** "ok" | "error" | "empty" */
  status: string
  count: number
  error?: string
}

export interface SearchResultSkills {
  items: MarketSkill[]
  total: number
  page: number
  hasMore: boolean
  nextPage?: string
  sourceStatuses?: SourceStatus[]
}

export interface Skill {
  id: string
  name: string
  description?: string
  directory: string
  contentHash?: string
  /** SKILL.md 单文件内容哈希（算法与市场侧一致），用于市场页内容级已安装匹配 */
  skillMdHash?: string
  boundAgents: string[] | null
  installedAt: string
  updatedAt: string
  /** 仓库来源信息（从 ~/.agents/.skill-lock.json 解析，用于更新检测） */
  repoOwner?: string
  repoName?: string
  repoBranch?: string
}

export interface UnmanagedSkill {
  agentId: string
  directory: string
  path: string
  /** skill 显示名称（从 SKILL.md 解析，缺省为 directory） */
  name?: string
  /** 所有发现该 skill 的路径（agent 目录 + ~/.agents/skills + SSOT） */
  foundIn?: string[]
}

export interface UpdateStatus {
  skillId: string
  directory: string
  localHash: string
  remoteHash: string
  hasUpdate: boolean
  checkedAt: string
  /** 与远端有差异的文件（相对技能目录），jsDelivr 内容级检测填充 */
  changedFiles?: string[]
  error?: string
  /** 来源未知，本次未执行更新检查 */
  sourceMissing?: boolean
  /** 来源有效，但本次未能定位远端技能内容 */
  skipped?: boolean
  skipReason?: string
}

export interface UpdateError {
  skillId: string
  directory: string
  error: string
}

export type ConflictKind = 'plain_same' | 'plain_diff' | 'orphan_link' | 'broken_link' | 'wrong_target'

export interface ReconcileItem {
  kind: ConflictKind
  skillId?: string
  directory: string
  path: string
  agentIds: string[]
  ssotHash?: string
  localHash?: string
  acknowledged: boolean
}

export interface SkillSourceBackfillResult {
  matched: string[]
  mismatched: string[]
  unmatched: string[]
  failed: string[]
}

function normalizeTheme(theme: string): Theme {
  return theme === 'light' || theme === 'dark' || theme === 'system' ? theme : 'system'
}

function normalizeSettings(settings: ConfigNS.Settings): Settings {
  const plain = optimizeToPlainObject(settings) as unknown as Record<string, unknown>
  return {
    ...(plain as unknown as Settings),
    theme: normalizeTheme(plain.theme as string),
  }
}

async function safeCall<T>(fn: () => Promise<T>): Promise<T> {
  try {
    return await fn()
  } catch (error) {
    throw ApiError.from(error)
  }
}

export const api = {
  agents: {
    list: async () => (await ListAgents() ?? []).filter(Boolean) as Agent[],
    rescan: async () => (await RescanAgents() ?? []).filter(Boolean) as Agent[],
    get: async (id: string) => optimizeToPlainObject(await GetAgent(id)),
    toggle: (id: string, enabled: boolean) => safeCall(() => ToggleAgent(id, enabled)),
    getMcpServers: async (id: string) => optimizeToPlainObject(await GetAgentMcpServers(id)),
  },
  mcp: {
    list: async () => (await ListMcpServers() ?? []) as McpServer[],
    get: async (id: string) => optimizeToPlainObject(await GetMcpServer(id)),
    add: (server: McpServer, agents: string[]) => safeCall(() => AddMcpServer(optimizeToPlainObject(server) as WailsMcpServer, agents)),
    update: (id: string, server: McpServer, agents: string[]) => safeCall(() => UpdateMcpServer(id, optimizeToPlainObject(server) as WailsMcpServer, agents)),
    delete: (id: string) => safeCall(() => DeleteMcpServer(id)),
    toggleAgent: (id: string, agentId: string, enabled: boolean) => safeCall(() => ToggleMcpServerAgent(id, agentId, enabled)),
    scan: async () => optimizeToPlainObject(await ScanMcpServers()) as ScanResult,
  },
  market: {
    searchServers: async (source: string, query: string, cursor = '', pageSize = 30) =>
      optimizeToPlainObject(await SearchMarketServers(source, query, cursor, pageSize)) as SearchResultServers,
    getServer: async (source: string, sourceId: string) =>
      optimizeToPlainObject(await GetMarketServer(source, sourceId)) as MarketServer,
    installServer: async (server: MarketServer, agents: string[]) =>
      optimizeToPlainObject(await InstallMarketServer(optimizeToPlainObject(server) as WailsMarketServer, agents)) as McpServer,
    searchSkills: async (query: string, pageSize = 30, page = 1, source = '') =>
      optimizeToPlainObject(await SearchMarketSkills(query, pageSize, page, source)) as SearchResultSkills,
    installSkill: async (skill: MarketSkill, agents: string[]) =>
      optimizeToPlainObject(await InstallMarketSkill(optimizeToPlainObject(skill) as MarketNS.MarketSkill, agents)) as Skill,
    getSkillRepos: async () => optimizeToPlainObject(await GetSkillRepos()) as SkillRepo[],
    addSkillRepo: (repo: SkillRepo) => safeCall(() => AddSkillRepo(optimizeToPlainObject(repo) as ConfigNS.SkillRepo)),
    removeSkillRepo: (repo: SkillRepo) => safeCall(() => RemoveSkillRepo(optimizeToPlainObject(repo) as ConfigNS.SkillRepo)),
    updateSkillRepo: (original: SkillRepo, updated: SkillRepo) =>
      safeCall(() => UpdateSkillRepo(
        optimizeToPlainObject(original) as ConfigNS.SkillRepo,
        optimizeToPlainObject(updated) as ConfigNS.SkillRepo
      )),
  },
  skills: {
    list: async () => optimizeToPlainObject(await ListSkills()) as Skill[],
    listCapableAgents: async () => optimizeToPlainObject(await ListSkillCapableAgents()) as Agent[],
    // 最近一次自动来源回填的结果（启动时后端后台执行；从未执行过时返回 null）
    lastBackfillResult: async (): Promise<SkillSourceBackfillResult | null> => {
      const [res, done] = await GetLastBackfillResult()
      if (!done) return null
      return {
        matched: res?.matched ?? [],
        mismatched: res?.mismatched ?? [],
        unmatched: res?.unmatched ?? [],
        failed: res?.failed ?? [],
      }
    },
    importDirectory: (path: string, agentIDs: string[]) => safeCall(() => ImportSkillDirectory(path, agentIDs)),
    installFromZip: (zipPath: string, agentIDs: string[]) => safeCall(() => InstallSkillFromZip(zipPath, agentIDs)),
    toggleAgent: (id: string, agentID: string, enabled: boolean) => safeCall(() => ToggleSkillAgent(id, agentID, enabled)),
    uninstall: (id: string) => safeCall(() => UninstallSkill(id)),
    resync: () => safeCall(async () => ResyncSkills()),
    migrateStorage: (target: string) => safeCall(async () => MigrateSkillStorage(target)),
    scanUnmanaged: async () => optimizeToPlainObject(await ScanUnmanagedSkills()) as UnmanagedSkill[],
    scanConflicts: async () => optimizeToPlainObject(await ScanSkillConflicts()) as ReconcileItem[],
    convertSkillCopy: (skillId: string, sourcePath: string) => safeCall(() => ConvertSkillCopyToLink(skillId, sourcePath)),
    overwriteSkillCopy: (skillId: string, sourcePath: string) => safeCall(() => OverwriteSkillCopyFromSSOT(skillId, sourcePath)),
    adoptSkillCopy: async (skillId: string, sourcePath: string) => optimizeToPlainObject(await AdoptSkillCopy(skillId, sourcePath)) as Skill,
    keepSkillFork: (skillId: string, sourcePath: string) => safeCall(() => KeepSkillFork(skillId, sourcePath)),
    cleanOrphanLinks: async () => {
      const removed = await CleanOrphanSkillLinks()
      return Array.isArray(removed) ? removed : []
    },
    checkUpdates: async () => optimizeToPlainObject(await CheckSkillUpdates()) as UpdateStatus[],
    updateSkill: async (skillId: string) => optimizeToPlainObject(await UpdateSkill(skillId)) as Skill,
    updateSkills: async (skillIds: string[]) => {
      const result = await UpdateSkills(skillIds)
      const plain = optimizeToPlainObject(result) as { updated?: Skill[]; errors?: UpdateError[] } | null
      return {
        updated: (plain?.updated ?? []) as Skill[],
        errors: (plain?.errors ?? []) as UpdateError[],
      }
    },
  },
  settings: {
    get: async () => normalizeSettings(await GetSettings()),
    update: (settings: Settings) => safeCall(() => UpdateSettings(optimizeToPlainObject(settings) as ConfigNS.Settings)),
  },
  backup: {
    create: (description: string) => safeCall(() => CreateBackupNow(description)),
    list: () => safeCall(async () => ListBackups()),
    restore: (id: string, opts: { applyMCP?: boolean; overwrite?: boolean; applyAgentStatus?: boolean; applySettings?: boolean }) =>
      safeCall(() => RestoreBackup(id, {
        ApplyMCP: opts.applyMCP ?? true,
        Overwrite: opts.overwrite ?? false,
        ApplyAgentStatus: opts.applyAgentStatus ?? false,
        ApplySettings: opts.applySettings ?? false,
      })),
    delete: (id: string) => safeCall(() => DeleteBackup(id)),
  },
  export: {
    exportData: (id: string, dest: string) => safeCall(() => ExportBackupToFile(id, dest)),
    importData: (src: string, opts: { overwrite?: boolean; applyAgentStatus?: boolean; applySettings?: boolean }) =>
      safeCall(() => ImportBackupFromFile(src, {
        ApplyMCP: true,
        Overwrite: opts.overwrite ?? false,
        ApplyAgentStatus: opts.applyAgentStatus ?? false,
        ApplySettings: opts.applySettings ?? false,
      })),
  },
  system: {
    openConfigFolder: () => safeCall(() => OpenConfigFolder()),
    pickFile: (filters: string) => safeCall(() => PickFile(filters)),
    pickDirectory: () => safeCall(() => PickDirectory()),
    setTheme: (theme: string) => safeCall(() => SetTheme(theme)),
    getStartupErrors: () => safeCall(() => GetStartupErrors() as Promise<string[]>),
    openUrl: (url: string) => {
      // 仅允许 http/https，防止恶意 scheme（如 file://、javascript:）被交给系统打开器执行。
      if (!/^https?:\/\//i.test(url)) return
      // safeCall 只转换错误类型不吞错：未处理的 rejection 会产生 unhandledrejection
      // 告警，且用户点击无任何反馈；显式 catch 静默（打开浏览器失败非关键路径）
      safeCall(() => OpenURL(url)).catch(() => {})
    },
    quit: () => safeCall(() => Quit()),
    hideWindow: () => safeCall(() => HideWindow()),
    showWindow: () => safeCall(() => ShowWindow()),
    notifyActivity: () => safeCall(() => NotifyActivity()),
    checkUpdate: async (): Promise<UpdateCheckResult> => {
      return optimizeToPlainObject(await safeCall(() => CheckUpdate())) as UpdateCheckResult
    },
    getAppVersion: async (): Promise<string> => {
      return safeCall(() => GetAppVersion())
    },
    startDownloadUpdate: async (url: string): Promise<void> => {
      return safeCall(() => StartDownloadUpdate(url))
    },
    pauseDownload: async (): Promise<void> => {
      return safeCall(() => PauseDownload())
    },
    resumeDownload: async (): Promise<void> => {
      return safeCall(() => ResumeDownload())
    },
    cancelDownload: async (): Promise<void> => {
      return safeCall(() => CancelDownload())
    },
    installUpdate: async (): Promise<void> => {
      return safeCall(() => InstallUpdate())
    },
  },
}

export const events = {
  on(event: string, callback: (...args: unknown[]) => void) {
    return Events.On(event, (ev) => {
      callback(ev.data)
    })
  },
  emit(event: string, ...args: unknown[]) {
    Events.Emit(event, args.length > 0 ? args[0] : undefined)
  },
}
