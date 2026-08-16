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

  // id → 是否 active（enabled/detected）的映射。
  // 合并组（mergedGroups）会包含 disabled 变体成员（如 CLI enabled + Desktop disabled），
  // 提交给后端的 id 列表必须按成员级过滤，否则 validateAgentIDs 会整体拒绝操作。
  const activeIds = computed(() => {
    const m = new Map<string, boolean>()
    for (const a of items.value) {
      m.set(a.id, a.status === 'enabled' || a.status === 'detected')
    }
    return m
  })

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

  // 按 name + configPath + variant 拆分的独立卡片组（用于 agents 页面）
  // 不合并 CLI/Desktop 变体，每个变体独立显示
  interface VariantGroup {
    id: string
    name: string
    variant: string
    status: AgentStatus
    configPath: string
  }
  const variantGroups = computed<VariantGroup[]>(() => {
    const groups: VariantGroup[] = []
    for (const a of items.value) {
      if (a.status === 'not_found') continue
      groups.push({
        id: a.id,
        name: a.name,
        variant: a.variant,
        status: a.status === 'enabled' || a.status === 'detected' ? 'enabled' : 'disabled',
        configPath: a.configPath,
      })
    }
    groups.sort((a, b) => a.name.localeCompare(b.name))
    return groups
  })

  // 包含 not_found agent 的合并组（用于设置页面显示所有 agent）
  const allMergedGroups = computed<AgentGroup[]>(() => {
    const map = new Map<string, Agent[]>()
    for (const a of items.value) {
      const key = `${a.name}|${a.configPath}`
      const list = map.get(key)
      if (list) list.push(a)
      else map.set(key, [a])
    }
    const groups: AgentGroup[] = []
    for (const [, members] of map) {
      const preferred = members.find(a => a.variant === 'cli') ?? members[0]
      const anyEnabled = members.some(a => a.status === 'enabled' || a.status === 'detected')
      const worstStatus = members.some(a => a.status === 'error')
        ? 'error'
        : members.some(a => a.status === 'not_found')
          ? (members.some(a => a.status !== 'not_found') ? 'enabled' : 'not_found')
          : anyEnabled ? 'enabled' : 'disabled'
      groups.push({
        ids: members.map(a => a.id),
        id: preferred.id,
        name: preferred.name,
        status: worstStatus,
        configPath: preferred.configPath,
      })
    }
    groups.sort((a, b) => {
      const aNotFound = a.status === 'not_found' ? 1 : 0
      const bNotFound = b.status === 'not_found' ? 1 : 0
      if (aNotFound !== bNotFound) return aNotFound - bNotFound
      return a.name.localeCompare(b.name)
    })
    return groups
  })

  // 串行化队列：fetch/rescan 通过 opChain 排队执行，杜绝并发请求
  // 响应乱序覆盖（旧响应晚到会把 items 打回过期状态，UI 与后端漂移）
  let opChain: Promise<void> = Promise.resolve()

  async function runList(op: () => Promise<Agent[]>, fromRescan: boolean) {
    loading.value = true
    error.value = null
    try {
      const list = await op()
      items.value = list
      if (fromRescan) {
        lastScanAt.value = new Date().toISOString()
      } else if (!lastScanAt.value) {
        // fetch 返回的是后端已扫描的当前状态，更新 lastScanAt 让 StatusBar
        // 不再停留在"正在检测..."
        lastScanAt.value = new Date().toISOString()
      }
    } catch (e) {
      const apiError = ApiError.from(e)
      error.value = apiError.message
      // 由调用方决定是否提示（fetch 静默、rescan/toggle rethrow）
      throw apiError
    } finally {
      loading.value = false
    }
  }

  function enqueue(op: () => Promise<Agent[]>, fromRescan: boolean): Promise<void> {
    const p = opChain.then(() => runList(op, fromRescan))
    // 吞掉错误使队列继续；调用方拿到的 p 仍会 reject
    opChain = p.catch(() => {})
    return p
  }

  async function fetch(force = false) {
    // loading 守卫防止重复刷新；但 toggle 后的刷新若被跳过会留下
    // UI 与后端不一致（切换开关后列表仍显示旧状态），因此允许强制排队刷新。
    if (loading.value && !force) return opChain
    return enqueue(() => api.agents.list(), false)
  }

  function rescan() {
    // 与 fetch 共享队列，避免并发请求互相覆盖结果（enqueue 串行化）。
    // loading 时排队执行而非静默跳过：scanSkills 等调用方把返回值当作
    // "已执行"，静默跳过会让后续 resync 基于过期 agent 列表，且 toast
    // 仍报"扫描完成"（与 fetch 的"刷新列表可跳过"语义不同——rescan
    // 必须真正重新扫描）。
    return enqueue(() => api.agents.rescan(), true)
  }

  async function toggle(id: string, enabled: boolean) {
    try {
      await api.agents.toggle(id, enabled)
      // 强制刷新：toggle 常被 UI 在 loading 已置位（初始扫描）时调用，
      // 默认守卫会跳过刷新导致 UI 与后端不一致
      await fetch(true)
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
    activeIds,
    sorted,
    mergedGroups,
    variantGroups,
    allMergedGroups,
    fetch,
    rescan,
    toggle,
    byId,
  }
})
