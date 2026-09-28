import { describe, it, expect, vi, afterAll } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

vi.mock('@/i18n', () => ({
  setLanguage: vi.fn(),
  resolveLanguage: (l: string) => l,
}))

vi.mock('@/lib/api', () => ({
  api: {
    settings: {
      get: vi.fn(),
      update: vi.fn(),
    },
    system: {
      setTheme: vi.fn(),
    },
  },
  ApiError: {
    from: (e: unknown) => ({ message: String(e) }),
  },
}))

import { api, type Settings } from '@/lib/api'
import { useSettingsStore } from './settings'

// settings store 的 applyTheme 依赖 DOM/window（node 测试环境无 jsdom），
// 提供最小桩即可——测试目标在 fetch/update 的守卫逻辑，不在主题应用本身。
function stubDomGlobals() {
  const root = {
    classList: { toggle: vi.fn() },
    style: {} as Record<string, string>,
  }
  const matchMedia = vi.fn(() => ({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }))
  vi.stubGlobal('document', { documentElement: root })
  vi.stubGlobal('window', { matchMedia })
  vi.stubGlobal('localStorage', { setItem: vi.fn() })
}

const baseSettings = {
  marketSources: { registry: { enabled: true } },
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
  logLevel: 'info',
}

function delayedResolved<T>(ms: number, value: T) {
  return new Promise<T>((resolve) => setTimeout(() => resolve(value), ms))
}

describe('settings store fetch/update race', () => {
  afterAll(() => {
    vi.unstubAllGlobals()
  })

  it('in-flight update during fetch must not be rolled back by stale snapshot', async () => {
    stubDomGlobals()
    setActivePinia(createPinia())
    const store = useSettingsStore()
    // 模拟进入设置页：onMounted 发出 fetch（后端快照里 theme 还是旧值 system）
    vi.mocked(api.settings.get).mockImplementationOnce(() =>
      delayedResolved(50, { ...baseSettings, theme: 'system' } as Settings),
    )
    vi.mocked(api.settings.update).mockImplementation(() => delayedResolved(80, undefined))

    const fetchP = store.fetch()

    // t≈10ms：模拟 withAutoSave——用户已乐观修改本地 config（theme→dark）
    // 并发起保存；保存仍在途（writeVersion 尚未递增）时 fetch 旧快照先返回
    setTimeout(() => {
      store.config.theme = 'dark'
      void store.update({ ...store.config })
    }, 10)

    // t=120ms：一切在途操作均已结束
    await new Promise((r) => setTimeout(r, 120))
    await fetchP

    // 字段级合并若被"fetch 写回的旧快照"污染，theme 会被回滚成 system
    expect(store.config.theme).toBe('dark')
  })

  it('fetch with no in-flight update loads config normally', async () => {
    stubDomGlobals()
    setActivePinia(createPinia())
    const store = useSettingsStore()
    vi.mocked(api.settings.get).mockImplementationOnce(() =>
      delayedResolved(30, { ...baseSettings, theme: 'light' } as Settings),
    )

    const fetchP = store.fetch()
    await new Promise((r) => setTimeout(r, 50))
    await fetchP

    expect(store.config.theme).toBe('light')
    expect(store.loaded).toBe(true)
  })
})
