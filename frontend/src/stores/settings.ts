import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { api, ApiError } from '@/lib/api'
import type { AppSettings } from '@/types'
import { WindowSetDarkTheme, WindowSetLightTheme, WindowSetSystemDefaultTheme } from '../../wailsjs/runtime/runtime'

const DEFAULT_SETTINGS: AppSettings = {
  theme: 'system',
  marketSources: {
    official: { enabled: true },
    github: { enabled: true },
    'skills-sh': { enabled: true },
    smithery: { enabled: true },
  },
  autoBackup: true,
  backupCount: 10,
  backupRetention: 50,
  skillStorage: 'agentpack',
  skillSyncMethod: 'symlink',
  skillRepos: [],
  windowAction: 'minimize',
  windowNoRemind: false,
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

  async function applyWailsTheme(theme: string) {
    try {
      // Go binding — most reliable on Windows
      await api.system.setTheme(theme)
    } catch (e) {
      console.warn('setTheme via Go binding failed:', e)
      // Fallback to Wails runtime JS API
      try {
        if (theme === 'dark') {
          await WindowSetDarkTheme()
        } else if (theme === 'light') {
          await WindowSetLightTheme()
        } else {
          await WindowSetSystemDefaultTheme()
        }
      } catch (e2) {
        console.warn('applyWailsTheme JS fallback failed:', e2)
      }
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
    loading.value = true
    try {
      const s = await api.settings.get()
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
    } catch (e) {
      const apiError = ApiError.from(e)
      error.value = apiError.message
    } finally {
      loading.value = false
    }
  }

  async function update(next: AppSettings) {
    try {
      await api.settings.update(next)
      config.value = next
      loaded.value = true
      await applyTheme(next.theme)
    } catch (e) {
      const apiError = ApiError.from(e)
      error.value = apiError.message
      throw apiError
    }
  }

  async function ensureLoaded() {
    if (loaded.value) return
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
