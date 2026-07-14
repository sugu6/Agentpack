import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, type Skill, type Agent, type UnmanagedSkill, type UpdateStatus, ApiError } from '@/lib/api'

export const useSkillsStore = defineStore('skills', () => {
  const skills = ref<Skill[]>([])
  const skillCapableAgents = ref<Agent[]>([])
  const unmanaged = ref<UnmanagedSkill[]>([])
  const loading = ref(false)
  const scanningUnmanaged = ref(false)
  const error = ref<string | null>(null)

  // 更新检测状态
  const updateStatuses = ref<UpdateStatus[]>([])
  const checkingUpdates = ref(false)
  const lastCheckedAt = ref<string | null>(null)

  async function fetchList(force = false) {
    if (!force && loading.value) return
    loading.value = true
    try {
      const [skillList, agents] = await Promise.all([
        api.skills.list(),
        api.skills.listCapableAgents(),
      ])
      skills.value = skillList
      skillCapableAgents.value = agents
    } catch (e) {
      const apiError = ApiError.from(e)
      error.value = apiError.message
    } finally {
      loading.value = false
    }
  }

  async function load() {
    return fetchList(false)
  }

  async function reload() {
    return fetchList(true)
  }

  function rebuildList(mutate: (list: Skill[]) => Skill[]) {
    skills.value = mutate([...skills.value])
  }

  async function withApiError<T>(fn: () => Promise<T>): Promise<T> {
    error.value = null
    try {
      return await fn()
    } catch (e) {
      const apiError = ApiError.from(e)
      error.value = apiError.message
      throw apiError
    }
  }

  async function importSkill(path: string, agentIDs: string[]) {
    const skill = await withApiError(() => api.skills.importDirectory(path, agentIDs))
    rebuildList(list => {
      list.push(skill)
      return list
    })
    return skill
  }

  async function toggleAgent(skillId: string, agentId: string, enabled: boolean) {
    await withApiError(() => api.skills.toggleAgent(skillId, agentId, enabled))
    rebuildList(list => {
      const set = new Set(list.find(s => s.id === skillId)?.boundAgents ?? [])
      if (enabled) set.add(agentId); else set.delete(agentId)
      return list.map(s => s.id === skillId
        ? { ...s, boundAgents: [...set].sort() }
        : s)
    })
  }

  async function uninstall(skillId: string) {
    await withApiError(() => api.skills.uninstall(skillId))
    rebuildList(list => list.filter(s => s.id !== skillId))
  }

  async function resync() {
    await withApiError(() => api.skills.resync())
  }

  async function migrateStorage(target: string) {
    await withApiError(async () => {
      await api.skills.migrateStorage(target)
      skills.value = [...(await api.skills.list())]
    })
  }

  async function scanUnmanaged() {
    if (scanningUnmanaged.value) return
    scanningUnmanaged.value = true
    try {
      unmanaged.value = await api.skills.scanUnmanaged()
    } catch (e) {
      const apiError = ApiError.from(e)
      error.value = apiError.message
      // 扫描失败时保留旧的 unmanaged 列表，不清空，让用户能区分"扫描失败"与"结果为空"
    } finally {
      scanningUnmanaged.value = false
    }
  }

  async function importUnmanaged(path: string, agentIDs: string[]) {
    const skill = await importSkill(path, agentIDs)
    unmanaged.value = unmanaged.value.filter(u => u.path !== path)
    return skill
  }

  async function installFromZip(zipPath: string, agentIDs: string[]) {
    await withApiError(async () => {
      await api.skills.installFromZip(zipPath, agentIDs)
      skills.value = [...(await api.skills.list())]
    })
  }

  // 检查已安装 skills 的远程更新
  // 首次检查仅记录基线（后端策略），后续检查才报告更新
  async function checkUpdates() {
    if (checkingUpdates.value) return
    checkingUpdates.value = true
    error.value = null
    try {
      const statuses = await api.skills.checkUpdates()
      const list = Array.isArray(statuses) ? statuses : []
      updateStatuses.value = list
      // 记录最近一次检查时间（取所有状态中的最大 checkedAt）
      let latest: string | null = null
      for (const s of list) {
        if (s.checkedAt && (!latest || s.checkedAt > latest)) latest = s.checkedAt
      }
      lastCheckedAt.value = latest
    } catch (e) {
      const apiError = ApiError.from(e)
      error.value = apiError.message
      throw apiError
    } finally {
      checkingUpdates.value = false
    }
  }

  // 根据 skillId 查询其更新状态
  function updateStatusOf(skillId: string): UpdateStatus | undefined {
    return updateStatuses.value.find(s => s.skillId === skillId)
  }

  function clearCache() {
    skills.value = []
    skillCapableAgents.value = []
    unmanaged.value = []
  }

  return {
    skills,
    skillCapableAgents,
    unmanaged,
    loading,
    scanningUnmanaged,
    error,
    updateStatuses,
    checkingUpdates,
    lastCheckedAt,
    load,
    reload,
    importSkill,
    toggleAgent,
    uninstall,
    resync,
    migrateStorage,
    scanUnmanaged,
    importUnmanaged,
    installFromZip,
    checkUpdates,
    updateStatusOf,
    clearCache,
  }
})
