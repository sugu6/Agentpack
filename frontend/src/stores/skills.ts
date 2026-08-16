import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
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
  const updatingSkillIds = ref<Set<string>>(new Set())
  const updatingAll = ref(false)

  // 加载在途时新请求被跳过（loading 守卫）：跳过意味着事件携带的变化
  // 未同步。置位后下一次 fetchList 补拉一次（强制），避免已安装标记/
  // 列表在事件丢失后长期陈旧（如 Market 页 reload 在途时 Skills 页卸载）。
  let pendingReload = false

  async function fetchList(force = false) {
    if (!force && loading.value) {
      pendingReload = true
      return
    }
    if (pendingReload) {
      pendingReload = false
      force = true
    }
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
    // 卸载后清除更新状态条目，否则 updateStatuses 残留已卸载技能的
    // hasUpdate 记录，"全部更新"会把它重新带进来并更新一个已卸载技能。
    updateStatuses.value = updateStatuses.value.filter(s => s.skillId !== skillId)
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

async function updateSkill(skillId: string) {
    // 单卡更新与"全部更新"共用后端同一批锁/目录操作，两套守卫必须互斥，
    // 否则全部更新在途时单卡更新会并发更新同一技能
    if (updatingSkillIds.value.has(skillId) || updatingAll.value) return
    const next = new Set(updatingSkillIds.value)
    next.add(skillId)
    updatingSkillIds.value = next
    error.value = null
    try {
      await api.skills.updateSkill(skillId)
      await fetchList(true)
      updateStatuses.value = updateStatuses.value.map(s =>
        s.skillId === skillId ? { ...s, hasUpdate: false } : s
      )
    } catch (e) {
      const apiError = ApiError.from(e)
      error.value = apiError.message
      throw apiError
    } finally {
      const nextIds = new Set(updatingSkillIds.value)
      nextIds.delete(skillId)
      updatingSkillIds.value = nextIds
    }
  }

async function updateAllSkills() {
    // 与单卡更新互斥（见 updateSkill 注释）：全部更新在途时单卡更新被
    // updatingAll 拦住，反向的守卫同样需要——单卡在途时启动全部更新会
    // 并发碰同一批技能/同一后端锁。
    if (updatingAll.value || updatingSkillIds.value.size > 0) return
    const updatableIds = updateStatuses.value
      .filter(s => s.hasUpdate)
      .map(s => s.skillId)
    if (updatableIds.length === 0) return

    updatingAll.value = true
    error.value = null
    try {
      const result = await api.skills.updateSkills(updatableIds)
      await fetchList(true)
      const updatedIds = new Set((result.updated ?? []).map((s: { id: string }) => s.id))
      // error 分支无需处理：失败技能保持 hasUpdate 不变，可再次尝试
      updateStatuses.value = updateStatuses.value.map(s =>
        updatedIds.has(s.skillId) ? { ...s, hasUpdate: false } : s,
      )
      return result
    } catch (e) {
      const apiError = ApiError.from(e)
      error.value = apiError.message
      throw apiError
    } finally {
      updatingAll.value = false
    }
  }

  // 根据 skillId 查询其更新状态
  // Map 索引：模板在 v-for 卡片中多次调用，线性 find 构成 O(N²) 渲染
  const updateStatusMap = computed(() => {
    const m = new Map<string, UpdateStatus>()
    for (const s of updateStatuses.value) m.set(s.skillId, s)
    return m
  })
  function updateStatusOf(skillId: string): UpdateStatus | undefined {
    return updateStatusMap.value.get(skillId)
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
    updatingSkillIds,
    updatingAll,
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
    updateSkill,
    updateAllSkills,
    updateStatusOf,
    clearCache,
  }
})
