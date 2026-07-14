import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { api, ApiError } from '@/lib/api'
import type { MarketServer, MarketSkill, SearchResultServers, SearchResultSkills } from '@/lib/api'
import { useSkillsStore } from './skills'

const EMPTY_SERVERS: SearchResultServers = { items: [], total: 0, page: 1, hasMore: false }
const EMPTY_SKILLS: SearchResultSkills = { items: [], total: 0, page: 1, hasMore: false }
const DEFAULT_PAGE_SIZE = 30

export const useMarketStore = defineStore('market', () => {
  const servers = ref<SearchResultServers>({ ...EMPTY_SERVERS })
  const skills = ref<SearchResultSkills>({ ...EMPTY_SKILLS })
  // MCP 搜索与 Skills 搜索使用各自独立的 loading 状态，
  // 避免一个 tab 的搜索进行时另一个 tab 误显示 Spinner 或禁用搜索按钮
  const loadingServers = ref(false)
  const loadingSkills = ref(false)
  const error = ref<string | null>(null)
  const currentSource = ref<string>('official')
  const currentQuery = ref<string>('')
  // Skills 搜索参数缓存，用于 loadMoreSkills
  const currentSkillSource = ref<string>('')
  const currentSkillQuery = ref<string>('')
  const currentSkillPageSize = ref(DEFAULT_PAGE_SIZE)

  // 单调递增的请求 ID，用于丢弃过期的搜索响应
  // 避免快速多次搜索时旧响应覆盖新响应
  // MCP 与 Skills 使用各自独立的计数器，避免跨 tab 互相丢弃响应
  let serverRequestId = 0
  let skillRequestId = 0

  const hasResults = computed(() => servers.value.items.length > 0)
  const hasSkillResults = computed(() => skills.value.items.length > 0)
  // Skills 搜索的来源状态（GitHub / skills.sh 各自的成功/失败/空结果）
  const sourceStatuses = computed(() => skills.value.sourceStatuses ?? [])

  async function search(source: string, query: string, cursor = '', pageSize = DEFAULT_PAGE_SIZE) {
    const requestId = ++serverRequestId
    loadingServers.value = true
    error.value = null
    currentSource.value = source
    currentQuery.value = query
    try {
      const result = await api.market.searchServers(source, query, cursor, pageSize)
      // 仅当这是最新的请求时才更新结果，丢弃过期响应
      if (requestId !== serverRequestId) return
      servers.value = result
    } catch (e) {
      if (requestId !== serverRequestId) return
      const apiError = ApiError.from(e)
      error.value = apiError.message
      servers.value = { ...EMPTY_SERVERS }
    } finally {
      if (requestId === serverRequestId) {
        loadingServers.value = false
      }
    }
  }

  async function loadMore() {
    if (!servers.value.hasMore || !servers.value.nextPage) return
    if (loadingServers.value) return
    loadingServers.value = true
    try {
      const more = await api.market.searchServers(
        currentSource.value,
        currentQuery.value,
        servers.value.nextPage,
        DEFAULT_PAGE_SIZE,
      )
      servers.value = {
        ...more,
        items: [...servers.value.items, ...more.items],
        page: servers.value.page + 1,
      }
    } catch (e) {
      const apiError = ApiError.from(e)
      error.value = apiError.message
    } finally {
      loadingServers.value = false
    }
  }

  async function installServer(server: MarketServer, agents: string[]) {
    try {
      return await api.market.installServer(server, agents)
    } catch (e) {
      const apiError = ApiError.from(e)
      error.value = apiError.message
      throw apiError
    }
  }

  // === Skills 市场 ===
  // 后端已合并双源（GitHub + skills.sh）并按下载量降序排序
  // 支持分页（无限滚动），searchSkills 重置到第 1 页，loadMoreSkills 累加后续页

  async function searchSkills(query: string, pageSize = DEFAULT_PAGE_SIZE, source = '') {
    const requestId = ++skillRequestId
    loadingSkills.value = true
    error.value = null
    currentSkillSource.value = source
    currentSkillQuery.value = query
    currentSkillPageSize.value = pageSize
    try {
      const result = await api.market.searchSkills(query, pageSize, 1, source)
      if (requestId !== skillRequestId) return
      skills.value = result
      // 等待已安装 skills 列表刷新完成，确保市场卡片正确显示「已安装」状态
      await useSkillsStore().reload()
    } catch (e) {
      if (requestId !== skillRequestId) return
      const apiError = ApiError.from(e)
      error.value = apiError.message
      skills.value = { ...EMPTY_SKILLS }
    } finally {
      if (requestId === skillRequestId) {
        loadingSkills.value = false
      }
    }
  }

  async function loadMoreSkills() {
    if (!skills.value.hasMore || !skills.value.nextPage) return
    if (loadingSkills.value) return
    const requestId = ++skillRequestId
    loadingSkills.value = true
    try {
      const page = parseInt(skills.value.nextPage, 10)
      const more = await api.market.searchSkills(
        currentSkillQuery.value,
        currentSkillPageSize.value,
        page,
        currentSkillSource.value,
      )
      if (requestId !== skillRequestId) return
      skills.value = {
        ...more,
        items: [...skills.value.items, ...more.items],
        page: more.page,
      }
      // 等待已安装 skills 列表刷新完成，确保市场卡片正确显示「已安装」状态
      await useSkillsStore().reload()
    } catch (e) {
      if (requestId !== skillRequestId) return
      const apiError = ApiError.from(e)
      error.value = apiError.message
    } finally {
      if (requestId === skillRequestId) {
        loadingSkills.value = false
      }
    }
  }

  async function installSkill(skill: MarketSkill, agents: string[]) {
    try {
      return await api.market.installSkill(skill, agents)
    } catch (e) {
      const apiError = ApiError.from(e)
      error.value = apiError.message
      throw apiError
    }
  }

  // 清空 Skills 搜索结果（用于切换到 skills.sh 来源时不自动搜索）
  function clearSkills() {
    skills.value = { ...EMPTY_SKILLS }
  }

  function clearCache() {
    servers.value = { ...EMPTY_SERVERS }
    skills.value = { ...EMPTY_SKILLS }
  }

  return {
    servers,
    skills,
    loadingServers,
    loadingSkills,
    error,
    currentSource,
    currentQuery,
    hasResults,
    hasSkillResults,
    sourceStatuses,
    search,
    loadMore,
    installServer,
    searchSkills,
    loadMoreSkills,
    installSkill,
    clearSkills,
    clearCache,
  }
})
