import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { api, ApiError, type Settings } from '@/lib/api'
import type { AppSettings } from '@/types'
import { setLanguage, resolveLanguage } from '@/i18n'


const DEFAULT_SETTINGS: AppSettings = {
  theme: 'system',
  marketSources: {
    registry: { enabled: true },
    github: { enabled: true },
    'skills-sh': { enabled: true },
  },
  autoBackup: true,
  backupCount: 10,
  backupRetention: 50,
  skillStorage: 'agentpack',
  skillSyncMethod: 'symlink',
  skillRepos: [],
  windowAction: 'minimize',
  windowNoRemind: false,
  language: '',
  liteAutoEnabled: false,
  liteAutoDelay: 5,
}

export const useSettingsStore = defineStore('settings', () => {
  const config = ref<AppSettings>({ ...DEFAULT_SETTINGS })
  const loading = ref(false)
  const loaded = ref(false)
  const error = ref<string | null>(null)
  let skillReposVersion = 0

  const theme = computed(() => config.value.theme)

  let mediaQuery: MediaQueryList | null = null
  let mediaHandler: ((e: MediaQueryListEvent) => void) | null = null

  // 保存中计数：每次 update 开始时 +1、结束时 -1。
  // 后端每次 update 成功都会广播 settings:changed，全局监听随即 fetch()。
  // 若本地有未落盘的修改（保存进行中/排队中），fetch 的无条件覆盖会冲掉
  // 用户刚输入的值，且排队的保存重放的也是被覆盖的旧值——输入静默丢失。
  let pendingWrite = 0
  // 写版本号：update 成功提交后自增。fetch 在发起时快照版本号，响应返回时
  // 若版本已变（请求期间有 update 提交）则丢弃陈旧响应——pendingWrite 只
  // 拦截"发起时"的请求，拦不住"请求在途期间"提交的保存。
  let writeVersion = 0
  // 在途的 fetch promise：ensureLoaded 命中 loading 时等待同一请求完成，
  // 而非立即返回导致调用方基于默认配置初始化。
  let inflight: Promise<Settings> | null = null

  async function applyWailsTheme(theme: string) {
    try {
      // Go binding — most reliable on Windows
      await api.system.setTheme(theme)
    } catch (e) {
      console.warn('setTheme via Go binding failed:', e)
      // v3: Runtime theme switching functions removed — theme is set at window creation time
    }
  }

  async function applyTheme(theme: string) {
    const root = document.documentElement
    const isDark =
      theme === 'dark' ||
      (theme === 'system' && window.matchMedia('(prefers-color-scheme: dark)').matches)
    root.classList.toggle('dark', isDark)
    // color-scheme 让浏览器原生控件（select option 列表、scrollbar、date picker 等）
    // 在暗色模式下自动适配，避免白底白字
    root.style.colorScheme = isDark ? 'dark' : 'light'

    // Persist to localStorage for next startup's inline script
    try { localStorage.setItem('agentpack-theme', theme) } catch {}

    // Sync Wails title bar theme (await to prevent race on rapid theme switches)
    await applyWailsTheme(theme)

    // Listen for system theme changes when in "system" mode
    setupSystemThemeListener(theme)
  }

  function setupSystemThemeListener(theme: string) {
    // Clean up existing listener
    if (mediaQuery && mediaHandler) {
      mediaQuery.removeEventListener('change', mediaHandler)
      mediaHandler = null
      mediaQuery = null
    }

    if (theme === 'system') {
      mediaQuery = window.matchMedia('(prefers-color-scheme: dark)')
      mediaHandler = (e: MediaQueryListEvent) => {
        document.documentElement.classList.toggle('dark', e.matches)
        document.documentElement.style.colorScheme = e.matches ? 'dark' : 'light'
        applyWailsTheme('system')
      }
      mediaQuery.addEventListener('change', mediaHandler)
    }
  }

  function dispose() {
    if (mediaQuery && mediaHandler) {
      mediaQuery.removeEventListener('change', mediaHandler)
      mediaHandler = null
      mediaQuery = null
    }
  }

  async function fetch() {
    if (loading.value) return
    // 本地有排队/进行中的保存时跳过覆盖：保存完成后 update() 已把最新
    // config 写入 store，后端旧快照此时覆盖只会回滚用户尚未确认的修改。
    if (pendingWrite > 0) return
    loading.value = true
    const p = api.settings.get()
    // 记录在途 promise，让并发调用方（ensureLoaded）能等待同一请求完成，
    // 避免其基于未加载的默认配置提前初始化（如 Market 页按默认源发请求）。
    inflight = p
    const requestVersion = writeVersion
    try {
      const s = await p
      // 请求在途期间有 update 成功提交：响应携带的是旧快照，丢弃避免覆盖
      // 刚保存的值（finally 仍会清 loading）。
      if (requestVersion !== writeVersion) return
      // Migrate legacy "auto" sync method to "symlink" before typed assignment.
      const rawSyncMethod = (s.skillSyncMethod as string | undefined) ?? 'symlink'
      const skillSyncMethod: AppSettings['skillSyncMethod'] = rawSyncMethod === 'copy' ? 'copy' : 'symlink'
      const rawWindowAction = s.windowAction as string | undefined
      const windowAction: AppSettings['windowAction'] = rawWindowAction === 'exit' ? 'exit' : 'minimize'
      const migrated: AppSettings = {
        ...DEFAULT_SETTINGS,
        ...s,
        marketSources: { ...DEFAULT_SETTINGS.marketSources, ...(s.marketSources ?? {}) },
        skillSyncMethod,
        windowAction,
      }
      config.value = migrated
      loaded.value = true
      await applyTheme(migrated.theme)
      // 同步 i18n 语言
      setLanguage(resolveLanguage(migrated.language))
    } catch (e) {
      const apiError = ApiError.from(e)
      error.value = apiError.message
    } finally {
      inflight = null
      loading.value = false
    }
  }

  async function update(next: AppSettings) {
    pendingWrite++
    try {
      await api.settings.update(next)
      writeVersion++
      // 合并保留旁路字段：SettingsView 的 refreshSkillRepos 会直接更新
      // config.skillRepos（跳过本 store 方法），在途 update() 的整体覆盖
      // （config.value = next）会抹掉这些变更，导致 UI 仓库列表凭空消失。
      // 以当前 config 中的 skillRepos 为准（它可能比 next 快照更新）。
      config.value = { ...next, skillRepos: config.value.skillRepos }
      loaded.value = true
      await applyTheme(next.theme)
      // 同步 i18n 语言(立即生效,不等 settings:changed 事件回环)
      setLanguage(resolveLanguage(next.language))
    } catch (e) {
      const apiError = ApiError.from(e)
      error.value = apiError.message
      throw apiError
    } finally {
      pendingWrite--
    }
  }

  async function ensureLoaded() {
    if (loaded.value) return
    // fetch 已在途：等待同一请求完成，避免基于默认配置提前返回。
    if (inflight) {
      try { await inflight } catch { /* 错误已记录到 error，调用方自行处理 */ }
      return
    }
    await fetch()
  }

  function setConfig(next: AppSettings) {
    config.value = next
  }

  // skillReposVersion 用于跨页面追踪仓库列表变更
  // Settings 页面添加/删除仓库后递增版本号，
  // Market 页面挂载时检测版本号变更，触发技能列表刷新
  function markSkillReposChanged() {
    skillReposVersion++
  }

  let lastCheckedSkillRepoVersion = 0

  function isSkillReposChanged(): boolean {
    const changed = skillReposVersion !== lastCheckedSkillRepoVersion
    lastCheckedSkillRepoVersion = skillReposVersion
    return changed
  }

  return {
    config,
    loading,
    loaded,
    error,
    theme,
    fetch,
    update,
    ensureLoaded,
    setConfig,
    applyTheme,
    dispose,
    markSkillReposChanged,
    isSkillReposChanged,
  }
})
