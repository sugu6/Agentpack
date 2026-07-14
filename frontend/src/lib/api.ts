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
  AddMcpServer,
  AddSkillRepo,
  CancelDownload,
  CheckSkillUpdates,
  CheckUpdate,
  DeleteMcpServer,
  OpenDownloadedFile,
  RemoveSkillRepo,
  StartDownloadUpdate,
  UpdateSkillRepo,
  UpdateMcpServer,
  UpdateSettings,
  SetTheme,
  HideWindow,
  Quit,
  ShowWindow,
} from '../../wailsjs/go/main/App'
import { EventsOn, EventsOff, EventsEmit, BrowserOpenURL } from '../../wailsjs/runtime/runtime'
import type { agents as AgentsNS, config as ConfigNS, market as MarketNS, mcp as McpNS } from '../../wailsjs/go/models'

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
  timeout?: number
  source: string
  sourceId?: string
  boundAgents: string[]
  installedAt: string
  updatedAt: string
}

export interface ScanItem {
  server: McpServer
  managed: boolean
  agentId: string
  agentName: string
  configPath: string
}

export interface ScanResult {
  items: ScanItem[]
  total: number
  managed: number
  newFound: number
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
  /** Smithery 特有字段（仅 source='smithery' 时填充，用于前端筛选） */
  bySmithery?: boolean
  isDeployed?: boolean
  isVerified?: boolean
  isRemote?: boolean
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
  boundAgents: string[]
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
  error?: string
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
    list: async () => optimizeToPlainObject(await ListAgents()),
    rescan: async () => optimizeToPlainObject(await RescanAgents()),
    get: async (id: string) => optimizeToPlainObject(await GetAgent(id)),
    toggle: (id: string, enabled: boolean) => safeCall(() => ToggleAgent(id, enabled)),
    getMcpServers: async (id: string) => optimizeToPlainObject(await GetAgentMcpServers(id)),
  },
  mcp: {
    list: async () => optimizeToPlainObject(await ListMcpServers()),
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
    importDirectory: (path: string, agentIDs: string[]) => safeCall(() => ImportSkillDirectory(path, agentIDs)),
    installFromZip: (zipPath: string, agentIDs: string[]) => safeCall(() => InstallSkillFromZip(zipPath, agentIDs)),
    toggleAgent: (id: string, agentID: string, enabled: boolean) => safeCall(() => ToggleSkillAgent(id, agentID, enabled)),
    uninstall: (id: string) => safeCall(() => UninstallSkill(id)),
    resync: () => safeCall(async () => ResyncSkills()),
    migrateStorage: (target: string) => safeCall(async () => MigrateSkillStorage(target)),
    scanUnmanaged: async () => optimizeToPlainObject(await ScanUnmanagedSkills()) as UnmanagedSkill[],
    checkUpdates: async () => optimizeToPlainObject(await CheckSkillUpdates()) as UpdateStatus[],
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
    openUrl: (url: string) => { BrowserOpenURL(url) },
    quit: () => safeCall(() => Quit()),
    hideWindow: () => safeCall(() => HideWindow()),
    showWindow: () => safeCall(() => ShowWindow()),
    checkUpdate: async (): Promise<UpdateCheckResult> => {
      return optimizeToPlainObject(await CheckUpdate()) as UpdateCheckResult
    },
    getAppVersion: async (): Promise<string> => {
      return GetAppVersion()
    },
    startDownloadUpdate: async (url: string): Promise<void> => {
      return safeCall(() => StartDownloadUpdate(url))
    },
    cancelDownload: async (): Promise<void> => {
      return safeCall(() => CancelDownload())
    },
    openDownloadedFile: async (filePath: string): Promise<void> => {
      return safeCall(() => OpenDownloadedFile(filePath))
    },
  },
}

export const events = {
  on(event: string, callback: (...args: unknown[]) => void) {
    return EventsOn(event, callback)
  },
  off(event: string) {
    EventsOff(event)
  },
  emit(event: string, ...args: unknown[]) {
    EventsEmit(event, ...args)
  },
}
