import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { api, type McpServer, type ScanResult, ApiError } from '@/lib/api'

export const useMcpStore = defineStore('mcp', () => {
  const items = ref<McpServer[]>([])
  const loading = ref(false)
  const error = ref<string | null>(null)
  const scanning = ref(false)
  const scanResult = ref<ScanResult | null>(null)

  const total = computed(() => items.value.length)

  // refresh 直接拉取列表，不走 loading 守卫。
  // mutation 成功后的刷新不应被正在进行的 fetch 跳过（否则会展示 stale data），
  // 且刷新失败不应掩盖主操作的成功。
  async function refresh() {
    try {
      items.value = await api.mcp.list()
    } catch (e) {
      // 刷新失败仅记录，不抛出，避免掩盖主操作的成功
      console.warn('mcp refresh failed:', ApiError.from(e).message)
    }
  }

  async function fetch() {
    if (loading.value) return
    loading.value = true
    error.value = null
    try {
      items.value = await api.mcp.list()
    } catch (e) {
      const apiError = ApiError.from(e)
      error.value = apiError.message
    } finally {
      loading.value = false
    }
  }

  async function withApiError<T>(fn: () => Promise<T>): Promise<T> {
    try {
      return await fn()
    } catch (e) {
      const apiError = ApiError.from(e)
      error.value = apiError.message
      throw apiError
    }
  }

  async function add(server: McpServer, agents: string[]) {
    await withApiError(() => api.mcp.add(server, agents))
    await refresh()
  }

  async function update(id: string, server: McpServer, agents: string[]) {
    await withApiError(() => api.mcp.update(id, server, agents))
    await refresh()
  }

  async function remove(id: string) {
    await withApiError(() => api.mcp.delete(id))
    await refresh()
  }

  async function toggleAgent(id: string, agentId: string, enabled: boolean) {
    await withApiError(() => api.mcp.toggleAgent(id, agentId, enabled))
    await refresh()
  }

  async function scan() {
    scanning.value = true
    scanResult.value = null
    try {
      const result = await api.mcp.scan()
      scanResult.value = result
      return result
    } catch (e) {
      const apiError = ApiError.from(e)
      error.value = apiError.message
      throw apiError
    } finally {
      scanning.value = false
    }
  }

  return {
    items,
    loading,
    error,
    scanning,
    scanResult,
    total,
    fetch,
    refresh,
    add,
    update,
    remove,
    toggleAgent,
    scan,
  }
})
