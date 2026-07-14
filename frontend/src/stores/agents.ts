import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { api, type Agent, ApiError } from '@/lib/api'
import type { AgentStatus } from '@/types'

export const useAgentsStore = defineStore('agents', () => {
  const items = ref<Agent[]>([])
  const loading = ref(false)
  const lastScanAt = ref<string | null>(null)
  const error = ref<string | null>(null)

  const detected = computed(() => items.value.filter((a) => a.status !== 'not_found'))
  const enabled = computed(() => items.value.filter((a) => a.status === 'enabled'))
  const active = computed(() => items.value.filter((a) => a.status === 'enabled' || a.status === 'detected'))
  const totalMcp = computed(() => items.value.reduce((s, a) => s + a.mcpCount, 0))

  const sorted = computed(() => {
    const list = [...items.value]
    list.sort((a, b) => {
      const aFound = a.status === 'not_found' ? 1 : 0
      const bFound = b.status === 'not_found' ? 1 : 0
      if (aFound !== bFound) return aFound - bFound
      return a.name.localeCompare(b.name)
    })
    return list
  })

  interface AgentGroup {
    ids: string[]
    id: string
    name: string
    status: AgentStatus
    configPath: string
  }
  const mergedGroups = computed<AgentGroup[]>(() => {
    const map = new Map<string, Agent[]>()
    for (const a of items.value) {
      if (a.status === 'not_found') continue
      const key = `${a.name}|${a.configPath}`
      const list = map.get(key)
      if (list) list.push(a)
      else map.set(key, [a])
    }
    const groups: AgentGroup[] = []
    for (const [, members] of map) {
      const preferred = members.find(a => a.variant === 'cli') ?? members[0]
      const anyEnabled = members.some(a => a.status === 'enabled' || a.status === 'detected')
      groups.push({
        ids: members.map(a => a.id),
        id: preferred.id,
        name: preferred.name,
        status: anyEnabled ? 'enabled' : 'disabled',
        configPath: preferred.configPath,
      })
    }
    groups.sort((a, b) => a.name.localeCompare(b.name))
    return groups
  })

  async function fetch() {
    if (loading.value) return
    loading.value = true
    error.value = null
    try {
      const list = await api.agents.list()
      items.value = list
      // fetch 返回的是后端已扫描的当前状态，更新 lastScanAt 让 StatusBar 不再停留在"正在检测..."
      if (!lastScanAt.value) lastScanAt.value = new Date().toISOString()
    } catch (e) {
      const apiError = ApiError.from(e)
      error.value = apiError.message
    } finally {
      loading.value = false
    }
  }

  async function rescan() {
    // 与 fetch 共享 loading 守卫，避免并发请求互相覆盖结果
    if (loading.value) return
    loading.value = true
    error.value = null
    try {
      const list = await api.agents.rescan()
      items.value = list
      lastScanAt.value = new Date().toISOString()
    } catch (e) {
      const apiError = ApiError.from(e)
      error.value = apiError.message
    } finally {
      loading.value = false
    }
  }

  async function toggle(id: string, enabled: boolean) {
    try {
      await api.agents.toggle(id, enabled)
      await fetch()
    } catch (e) {
      const apiError = ApiError.from(e)
      error.value = apiError.message
      throw apiError
    }
  }

  function byId(id: string) {
    return items.value.find((a) => a.id === id)
  }

  return {
    items,
    loading,
    error,
    lastScanAt,
    detected,
    enabled,
    active,
    totalMcp,
    sorted,
    mergedGroups,
    fetch,
    rescan,
    toggle,
    byId,
  }
})
