<script setup lang="ts">
import { ref, onMounted, onActivated, onDeactivated, watch, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useSkillsStore } from '@/stores/skills'
import { useAgentsStore } from '@/stores/agents'
import { Card, CardContent, Button, Badge, Spinner, Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from '@/components/ui'
import { PhTrash, PhSparkle, PhMagnifyingGlass, PhFileArchive, PhFolderOpen, PhArrowClockwise, PhArrowUp } from '@phosphor-icons/vue'
import { api, events, ApiError } from '@/lib/api'
import { normalizeVariant, variantToBadge, agentDisplayName } from '@/composables/useAgentHelpers'
import AgentToggleButton from '@/components/agent/AgentToggleButton.vue'
import { useConfirm } from '@/composables/useConfirm'
import { useToast } from '@/composables/useToast'

const { t } = useI18n()
const skills = useSkillsStore()
const agents = useAgentsStore()
const confirm = useConfirm()
const toast = useToast()
const scanning = ref(false)
const importingZip = ref(false)

// "导入已有"对话框
const showImportExisting = ref(false)
const importingUnmanaged = ref(false)

// 每个未管理 skill 的导入配置：选中的 skill path → 目标 agent IDs
// 默认只启用来源 agent
const importConfig = ref<Map<string, Set<string>>>(new Map())

// "从 zip 安装"的 Agent 选择对话框
const showAgentSelector = ref(false)
const pendingZipPath = ref<string | null>(null)
const selectedAgentIds = ref<Set<string>>(new Set())
let unsubscribeSkillsChanged: (() => void) | undefined

// Skill-capable agent groups (filter mergedGroups by skillCapableAgents)
const skillCapableGroups = computed(() => {
  const capableIds = new Set(skills.skillCapableAgents.map(a => a.id))
  return agents.mergedGroups.filter(g => g.ids.some(id => capableIds.has(id)))
})

// 有更新的 skill 数量
const updatesCount = computed(() =>
  skills.updateStatuses.filter(s => s.hasUpdate).length,
)

// 可绑定目标：合并组可能包含 disabled 变体成员，仅取后端返回的 capable
// （skillCapableAgents 只含 enabled/detected）成员，避免提交后被拒绝。
const allCapableAgentIds = computed(() => {
  const capable = new Set(skills.skillCapableAgents.map(a => a.id))
  return skillCapableGroups.value.flatMap(g => g.ids.filter(id => capable.has(id)))
})

// agent ID → name 映射
const agentNameMap = computed(() => {
  const m = new Map<string, string>()
  for (const a of skills.skillCapableAgents) m.set(a.id, a.name)
  return m
})

// 未管理 skills 列表（按 directory 去重展示，记录所有来源 agent）
const unmanagedList = computed(() => {
  const map = new Map<string, { directory: string; name: string; path: string; agentIds: string[] }>()
  for (const u of skills.unmanaged) {
    const existing = map.get(u.directory)
    if (existing) {
      if (u.agentId && !existing.agentIds.includes(u.agentId)) {
        existing.agentIds.push(u.agentId)
      }
    } else {
      map.set(u.directory, {
        directory: u.directory,
        name: u.name || u.directory,
        path: u.path,
        agentIds: u.agentId ? [u.agentId] : [],
      })
    }
  }
  return [...map.values()].sort((a, b) => a.directory.localeCompare(b.directory))
})

// 选中的 skill paths
const selectedPaths = computed(() => {
  const paths: string[] = []
  for (const [path, agents] of importConfig.value) {
    if (agents.size > 0) paths.push(path)
  }
  return paths
})

function isPathSelected(path: string): boolean {
  const set = importConfig.value.get(path)
  return !!set && set.size > 0
}

function toggleImportSelect(path: string, agentIds: string[]) {
  const next = new Map(importConfig.value)
  const existing = next.get(path)
  if (existing && existing.size > 0) {
    next.delete(path)
  } else {
    // 默认只启用来源 agent
    next.set(path, new Set(agentIds))
  }
  importConfig.value = next
}

function toggleImportAgentExplicit(path: string, agentId: string, enabled: boolean, defaultAgentIds: string[]) {
  const next = new Map(importConfig.value)
  let current = next.get(path)
  // 如果 skill 还未勾选，自动初始化
  if (!current) {
    current = new Set(defaultAgentIds)
    next.set(path, current)
  }
  const updated = new Set(current)
  if (enabled) {
    updated.add(agentId)
  } else {
    updated.delete(agentId)
  }
  // 如果全部取消，移除整个条目（取消勾选状态）
  if (updated.size === 0) {
    next.delete(path)
  } else {
    next.set(path, updated)
  }
  importConfig.value = next
}

function toggleImportGroup(path: string, group: { ids: string[] }, enabled: boolean, defaultAgentIds: string[]) {
  const next = new Map(importConfig.value)
  let current = next.get(path)
  if (!current) {
    current = new Set(defaultAgentIds)
    next.set(path, current)
  }
  const updated = new Set(current)
  for (const id of group.ids) {
    if (enabled) updated.add(id)
    else updated.delete(id)
  }
  if (updated.size === 0) {
    next.delete(path)
  } else {
    next.set(path, updated)
  }
  importConfig.value = next
}

function toggleSelectAll(checked: boolean) {
  if (checked) {
    selectedAgentIds.value = new Set(allCapableAgentIds.value)
  } else {
    selectedAgentIds.value = new Set()
  }
}

function toggleAgentSelect(id: string) {
  const next = new Set(selectedAgentIds.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  selectedAgentIds.value = next
}

function toggleGroupSelect(group: { ids: string[] }, enabled: boolean) {
  const next = new Set(selectedAgentIds.value)
  const capable = new Set(skills.skillCapableAgents.map(a => a.id))
  for (const id of group.ids) {
    if (enabled) {
      // 只添加可绑定成员（capable 集合已限定 enabled/detected）
      if (capable.has(id)) next.add(id)
    } else {
      next.delete(id)
    }
  }
  selectedAgentIds.value = next
}

function isGroupBound(boundAgents: string[] | null, group: { ids: string[] }) {
  return group.ids.some(id => boundAgents?.includes(id) ?? false)
}

async function toggleGroup(skillId: string, group: { ids: string[] }, enabled: boolean) {
  try {
    // 只绑定可绑定目标：disabled 变体成员不在 capable 列表，提交会被后端整体或局部拒绝
    const capable = new Set(skills.skillCapableAgents.map(a => a.id))
    const uniqueIds = [...new Set(group.ids)].filter(id => capable.has(id))
    const results = await Promise.allSettled(
      uniqueIds.map((agentId) => skills.toggleAgent(skillId, agentId, enabled))
    )
    const failures = results.filter((r) => r.status === 'rejected')
    if (failures.length > 0) {
      const msg = failures.map((r) => (r as PromiseRejectedResult).reason?.message || t('skills.toast.toggleFailed')).join('; ')
      toast.warning(t('skills.toast.toggleBindingPartialFailed', { error: msg }))
    }
  } catch (e) {
    const apiError = ApiError.from(e)
    toast.error(t('skills.toast.toggleBindingFailed', { error: apiError.message }))
  }
}

let needsReloadAfterLoad = false

function onSkillsChanged() {
  if (skills.loading) {
    needsReloadAfterLoad = true
    return
  }
  skills.load().catch((e: unknown) => console.warn('reload skills failed:', e))
}

// 监听 skills.loading 从 true 变 false：若 load 期间有 skills:changed 到达则重新加载，
// 避免事件被永久丢弃（I-13）。覆盖初始加载与后续所有 load（如 scanSkills 触发的）。
watch(() => skills.loading, (loading, prev) => {
  if (prev && !loading && needsReloadAfterLoad) {
    needsReloadAfterLoad = false
    skills.load().catch((e: unknown) => console.warn('deferred reload skills failed:', e))
  }
})

onMounted(async () => {
  await skills.load()
})

// KeepAlive 下用 onActivated/onDeactivated 管理事件订阅，避免缓存视图与 App.vue 双重处理
onActivated(() => {
  unsubscribeSkillsChanged = events.on('skills:changed', onSkillsChanged)
})

onDeactivated(() => {
  unsubscribeSkillsChanged?.()
})

// "导入已有"：打开弹窗，先扫描 agent 目录中的未管理 skills
async function openImportExisting() {
  showImportExisting.value = true
  importConfig.value = new Map()
  await skills.scanUnmanaged()
}

// 批量导入选中的未管理 skills，每个 skill 用各自的 agent 配置
async function confirmImportExisting() {
  if (selectedPaths.value.length === 0) return

  importingUnmanaged.value = true
  try {
    const results = await Promise.allSettled(
      selectedPaths.value.map(p => {
        const agentIDs = [...(importConfig.value.get(p) || [])]
        return skills.importUnmanaged(p, agentIDs)
      })
    )
    const failures = results.filter(r => r.status === 'rejected')
    const successCount = results.length - failures.length
    if (successCount > 0) {
      toast.success(t('skills.toast.importSuccess', { count: successCount }))
    }
    if (failures.length > 0) {
      toast.warning(t('skills.toast.importFailedCount', { count: failures.length }))
    }
    await skills.load()
    showImportExisting.value = false
  } catch (e: unknown) {
    toast.error(toast.fromError(e, t('skills.toast.importFailed')))
  } finally {
    importingUnmanaged.value = false
  }
}

// "从 zip 安装"：选择 zip 文件后弹出 Agent 选择
async function installFromZip() {
  try {
    const zipPath = await api.system.pickFile('*.zip')
    if (!zipPath) return

    const agentIDs = allCapableAgentIds.value
    if (agentIDs.length === 0) {
      toast.info(t('skills.noCapableAgents'))
      return
    }

    selectedAgentIds.value = new Set()
    pendingZipPath.value = zipPath
    showAgentSelector.value = true
  } catch (e: unknown) {
    // 用户取消文件选择是正常操作，不应提示错误（原生对话框取消时返回 "cancelled by user"）
    if (isFilePickCancelled(e)) return
    toast.error(toast.fromError(e, t('skills.toast.pickFileFailed')))
  }
}

// 判断是否为用户取消文件选择
function isFilePickCancelled(e: unknown): boolean {
  const msg = (e instanceof Error ? e.message : String(e ?? '')).toLowerCase()
  return msg === '' || msg.includes('cancel')
}

async function confirmZipImport() {
  const zipPath = pendingZipPath.value
  const agentIDs = [...selectedAgentIds.value]
  if (!zipPath || agentIDs.length === 0) return

  showAgentSelector.value = false
  pendingZipPath.value = null
  importingZip.value = true
  try {
    await skills.installFromZip(zipPath, agentIDs)
    await skills.load()
    toast.success(t('skills.toast.zipInstallSuccess'))
  } catch (e: unknown) {
    toast.error(toast.fromError(e, t('skills.toast.zipInstallFailed')))
  } finally {
    importingZip.value = false
  }
}

async function uninstallSkill(id: string) {
  const skill = skills.skills.find(s => s.id === id)
  const ok = await confirm.confirm({
    title: t('dialog.confirm.uninstall'),
    message: skill
      ? t('skills.uninstallConfirmMessageNamed', { name: skill.name })
      : t('skills.uninstallConfirmMessage'),
    confirmText: t('common.uninstall'),
    variant: 'destructive',
  })
  if (!ok) return
  try {
    await skills.uninstall(id)
    toast.success(t('toast.uninstallSuccess'))
  } catch (e: unknown) {
    toast.error(toast.fromError(e, t('toast.uninstallFailed')))
  }
}

// 检查更新：调用后端比对远程 GitHub Trees SHA
// 首次检查仅记录基线，后续检查才报告更新
async function onCheckUpdates() {
  if (skills.checkingUpdates) return
  try {
    await skills.checkUpdates()
    // 局部变量改名避免遮蔽组件级 computed updatesCount（非响应式局部值）
    const newUpdatesCount = skills.updateStatuses.filter(s => s.hasUpdate).length
    const errorCount = skills.updateStatuses.filter(s => s.error).length
    if (newUpdatesCount > 0) {
      toast.info(t('skills.toast.updatesFound', { count: newUpdatesCount }))
    } else if (errorCount > 0 && errorCount === skills.updateStatuses.length) {
      // 所有 skill 都检测失败，可能是 rate limit
      toast.warning(t('update.message.rateLimited'))
    } else if (errorCount > 0) {
      toast.info(t('skills.toast.partialCheckFailed', { count: errorCount }))
    } else {
      toast.success(t('skills.toast.allUpToDate'))
    }
  } catch (e: unknown) {
    const apiError = ApiError.from(e)
    const msg = apiError.message
    // 限流友好提示
    if (msg.includes('rate limit') || msg.includes('403') || msg.includes('429') || msg.includes('too many') || msg.includes('限流')) {
      toast.warning(t('update.message.rateLimited'))
    } else {
      toast.error(t('skills.toast.checkUpdatesFailed', { error: msg }))
    }
  }
}

async function onUpdateSkill(skillId: string) {
  try {
    await skills.updateSkill(skillId)
    toast.success(t('skills.toast.updateSuccess'))
  } catch (e: unknown) {
    const apiError = ApiError.from(e)
    toast.error(t('skills.toast.updateFailed', { error: apiError.message }))
  }
}

async function onUpdateAll() {
  const updatableCount = skills.updateStatuses.filter(s => s.hasUpdate).length
  if (updatableCount === 0) return

  const ok = await confirm.confirm({
    title: t('skills.updateAll'),
    message: t('skills.confirmUpdateAll', { count: updatableCount }),
    confirmText: t('skills.update'),
    variant: 'default',
  })
  if (!ok) return

  try {
    const result = await skills.updateAllSkills()
    const successCount = result?.updated?.length ?? 0
    const errorCount = result?.errors?.length ?? 0
    if (errorCount > 0) {
      toast.warning(t('skills.toast.updateAllPartial', { success: successCount, failed: errorCount }))
    } else {
      toast.success(t('skills.toast.updateAllSuccess', { count: successCount }))
    }
  } catch (e: unknown) {
    const apiError = ApiError.from(e)
    toast.error(t('skills.toast.updateFailed', { error: apiError.message }))
  }
}

async function scanSkills() {
  try {
    scanning.value = true
    // 1. Refresh agent list
    await agents.rescan()
    // 2. Resync bound skills (re-sync broken symlinks, clean orphans)
    await skills.resync()
    // 3. Reload managed skills list
    await skills.load()
    // 4. Scan unmanaged skills in global ~/.agents/skills (read-only)
    await skills.scanUnmanaged()
    toast.success(t('skills.toast.scanComplete'))
  } catch (e: unknown) {
    toast.error(toast.fromError(e, t('skills.toast.scanFailed')))
  } finally {
    scanning.value = false
  }
}
</script>

<template>
  <div class="flex h-full flex-col">
    <!-- Fixed header -->
    <div class="shrink-0 border-b border-border px-8 pt-8 pb-4">
      <div class="mx-auto max-w-6xl">
        <div class="flex items-baseline justify-between gap-4">
          <h1 class="text-2xl font-semibold tracking-tight">{{ t('nav.skills') }}</h1>
          <p class="text-sm text-muted-foreground">{{ t('skills.subtitle') }}</p>
        </div>
        <div class="mt-3 flex justify-end gap-2">
          <Button variant="outline" size="sm" :disabled="importingUnmanaged" @click="openImportExisting">
            <PhFolderOpen :size="14" :class="{ 'animate-pulse': importingUnmanaged }" />
            <span>{{ importingUnmanaged ? t('skills.importing') : t('skills.importExisting') }}</span>
          </Button>
          <Button variant="outline" size="sm" :disabled="importingZip" @click="installFromZip">
            <PhFileArchive :size="14" :class="{ 'animate-pulse': importingZip }" />
            <span>{{ importingZip ? t('skills.installing') : t('skills.installFromZip') }}</span>
          </Button>
          <Button variant="outline" size="sm" :disabled="scanning" @click="scanSkills">
            <PhMagnifyingGlass :size="14" :class="{ 'animate-pulse': scanning }" />
            <span>{{ scanning ? t('skills.scanning') : t('skills.scan') }}</span>
          </Button>
          <Button v-if="skills.updateStatuses.some(s => s.hasUpdate)" variant="default" size="sm" :disabled="skills.updatingAll" @click="onUpdateAll">
            <PhArrowUp :size="14" :class="{ 'animate-spin': skills.updatingAll }" />
            <span>{{ skills.updatingAll ? t('skills.updatingAll') : t('skills.updateAll') }}</span>
          </Button>
          <Button v-else variant="outline" size="sm" :disabled="skills.checkingUpdates" @click="onCheckUpdates">
            <PhArrowClockwise :size="14" :class="{ 'animate-spin': skills.checkingUpdates }" />
            <span>{{ skills.checkingUpdates ? t('skills.checkingUpdates') : t('skills.checkUpdates') }}</span>
          </Button>
        </div>
      </div>

      <div class="mt-4 grid grid-cols-3 gap-3">
        <Card class="bg-card/50">
          <CardContent class="p-4">
            <div class="text-xs uppercase tracking-wider text-muted-foreground">{{ t('common.installed') }}</div>
            <div class="mt-1 text-2xl font-semibold tabular-nums">{{ skills.skills.length }}</div>
          </CardContent>
        </Card>
        <Card class="bg-card/50">
          <CardContent class="p-4">
            <div class="text-xs uppercase tracking-wider text-muted-foreground">{{ t('common.activeAgents') }}</div>
            <div class="mt-1 text-2xl font-semibold tabular-nums">{{ agents.active.length }}</div>
          </CardContent>
        </Card>
        <Card class="bg-card/50">
          <CardContent class="p-4">
            <div class="text-xs uppercase tracking-wider text-muted-foreground">{{ t('skills.updatable') }}</div>
            <div class="mt-1 text-2xl font-semibold tabular-nums" :class="{ 'text-warning': updatesCount > 0 }">{{ updatesCount }}</div>
          </CardContent>
        </Card>
      </div>
    </div>

    <!-- Scrollable content -->
    <div class="flex-1 overflow-y-auto">
      <div class="mx-auto max-w-6xl space-y-2 px-8 py-4">
        <!-- Empty state -->
        <div v-if="skills.skills.length === 0 && !skills.loading" class="py-12 text-center">
          <PhSparkle :size="48" class="mx-auto mb-4 text-muted-foreground/30" />
          <p class="text-muted-foreground">{{ t('skills.emptyTitle') }}</p>
          <p class="mt-1 text-sm text-muted-foreground">{{ t('skills.emptyDescription') }}</p>
        </div>

        <!-- Loading -->
        <div v-if="skills.loading" class="py-8 text-center text-sm text-muted-foreground">
          {{ t('common.loading') }}
        </div>

        <!-- Skill cards -->
        <Card v-for="skill in skills.skills" :key="skill.id">
          <CardContent class="p-4">
            <div class="flex items-start gap-4">
              <div class="flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary">
                <PhSparkle :size="16" weight="fill" />
              </div>
              <div class="min-w-0 flex-1">
                <div class="flex items-center gap-2">
                  <h3 class="text-sm font-semibold">{{ skill.name }}</h3>
                  <Badge variant="outline">{{ skill.directory }}</Badge>
                  <Badge v-if="skills.updatingSkillIds.has(skill.id)" variant="outline">{{ t('skills.updating') }}</Badge>
                  <Badge v-else-if="skills.updateStatusOf(skill.id)?.hasUpdate" variant="warning">{{ t('skills.hasUpdate') }}</Badge>
                  <Badge v-else-if="skills.updateStatusOf(skill.id)?.error" variant="destructive" :title="skills.updateStatusOf(skill.id)!.error">{{ t('skills.checkFailed') }}</Badge>
                  <span v-if="skill.boundAgents && skill.boundAgents.length > 0" class="text-[11px] text-muted-foreground">{{ t('skills.boundAgentCount', { count: skill.boundAgents.length }) }}</span>
                </div>

                <div class="mt-3 flex flex-nowrap items-center gap-2 border-t border-border pt-3">
                  <div
                    v-for="group in skillCapableGroups"
                    :key="group.id"
                    class="flex shrink-0 items-center"
                  >
                    <AgentToggleButton
                      :agent-id="group.id"
                      :agent-name="group.ids.length > 1 ? group.name : agentDisplayName({ name: group.name, id: group.id })"
                      :model-value="isGroupBound(skill.boundAgents, group)"
                      :disabled="group.status !== 'enabled'"
                      :badge="group.ids.length > 1 ? null : variantToBadge(normalizeVariant(undefined, group.id))"
                      @update:model-value="(v: boolean) => toggleGroup(skill.id, group, v)"
                    />
                  </div>
                  <span v-if="skillCapableGroups.length === 0" class="text-[11px] text-muted-foreground">
                    {{ t('skills.noCapableAgents') }}
                  </span>
                </div>
              </div>
              <div class="flex shrink-0 gap-2">
                <Button
                  v-if="skills.updateStatusOf(skill.id)?.hasUpdate"
                  variant="outline"
                  size="icon"
                  class="text-primary hover:bg-primary/10"
                  :disabled="skills.updatingSkillIds.has(skill.id) || skills.updatingAll"
                  :aria-busy="skills.updatingSkillIds.has(skill.id)"
                  :aria-label="t('skills.update')"
                  :title="skills.updatingSkillIds.has(skill.id) ? t('skills.updating') : skills.updatingAll ? t('skills.updatingAll') : t('skills.update')"
                  @click="onUpdateSkill(skill.id)"
                >
                  <Spinner v-if="skills.updatingSkillIds.has(skill.id)" class="size-3.5" />
                  <PhArrowUp v-else :size="14" />
                </Button>
                
                <Button variant="outline" size="icon" class="border-destructive/40 text-destructive hover:bg-destructive/10" :aria-label="t('common.uninstall')" @click="uninstallSkill(skill.id)">
                  <PhTrash :size="14" class="text-destructive" />
                </Button>
              </div>
            </div>
          </CardContent>
        </Card>

        <p v-if="skills.error" class="mt-4 text-xs text-destructive">
          {{ skills.error }}
        </p>
      </div>
    </div>

    <!-- "导入已有"对话框 -->
    <Dialog v-model:open="showImportExisting">
      <DialogContent class="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{{ t('skills.importExistingTitle') }}</DialogTitle>
          <DialogDescription>{{ t('skills.importExistingDesc') }}</DialogDescription>
        </DialogHeader>

        <!-- 工具栏 -->
        <div v-if="unmanagedList.length > 0" class="flex items-center justify-between border-b border-border pb-2">
          <label class="flex items-center gap-2 text-xs text-muted-foreground cursor-pointer">
            <input
              type="checkbox"
              :checked="selectedPaths.length === unmanagedList.length"
              :indeterminate="selectedPaths.length > 0 && selectedPaths.length < unmanagedList.length"
              @change="(e: Event) => {
                if ((e.target as HTMLInputElement).checked) {
                  const next = new Map()
                  unmanagedList.forEach(u => next.set(u.path, new Set(u.agentIds)))
                  importConfig = next
                } else {
                  importConfig = new Map()
                }
              }"
              class="h-3.5 w-3.5"
            />
            {{ t('common.selectAll') }}
          </label>
          <span class="text-xs text-muted-foreground">{{ t('skills.selectedCount', { selected: selectedPaths.length, total: unmanagedList.length }) }}</span>
        </div>

        <div class="max-h-[55vh] space-y-2 overflow-y-auto py-2">
          <div
            v-for="u in unmanagedList"
            :key="u.path"
            class="rounded-md border p-3 transition-colors"
            :class="isPathSelected(u.path) ? 'border-primary/40 bg-primary/5' : 'border-border'"
          >
            <!-- 标题行 -->
            <div class="flex items-start gap-3">
              <input
                type="checkbox"
                :checked="isPathSelected(u.path)"
                @change="toggleImportSelect(u.path, u.agentIds)"
                class="mt-0.5 h-4 w-4 shrink-0"
              />
              <div class="min-w-0 flex-1">
                <div class="flex items-center gap-2">
                  <span class="text-sm font-semibold">{{ u.name }}</span>
                  <Badge variant="outline" class="text-[10px]">{{ u.directory }}</Badge>
                </div>
                <p v-if="u.agentIds.length > 1" class="mt-0.5 text-[10px] text-muted-foreground/70">
                  {{ u.agentIds.map(id => agentNameMap.get(id) || id).join(', ') }}
                </p>
                <p v-else class="mt-0.5 text-[10px] text-muted-foreground/70">
                  {{ t('skills.sourceLine', { agents: u.agentIds.length > 0 ? u.agentIds.map(id => agentNameMap.get(id) || id).join(', ') : t('common.unknown') }) }}
                </p>
              </div>
            </div>

            <!-- Agent 开关（始终显示，合并同组 Agent） -->
            <div class="mt-3 flex flex-wrap items-center gap-2 border-t border-border/50 pt-2 pl-7">
              <div
                v-for="group in skillCapableGroups"
                :key="group.id"
                class="flex items-center gap-1.5"
              >
                <AgentToggleButton
                  :agent-id="group.id"
                  :agent-name="group.ids.length > 1 ? group.name : agentDisplayName({ name: group.name, id: group.id })"
                  :model-value="group.ids.some(id => importConfig.get(u.path)?.has(id))"
                  :disabled="group.status !== 'enabled'"
                  :badge="group.ids.length > 1 ? null : variantToBadge(normalizeVariant(undefined, group.id))"
                  @update:model-value="(v: boolean) => toggleImportGroup(u.path, group, v, u.agentIds)"
                />
                <Badge v-if="u.agentIds.length <= 1 && group.ids.some(id => u.agentIds.includes(id))" variant="secondary" class="text-[9px] px-1 py-0">{{ t('skills.source') }}</Badge>
              </div>
            </div>
          </div>

          <div v-if="unmanagedList.length === 0" class="py-8 text-center text-sm text-muted-foreground">
            <PhMagnifyingGlass :size="28" class="mx-auto mb-3 text-muted-foreground/30" />
            <p>{{ t('skills.noImportable') }}</p>
            <p class="mt-1 text-xs">{{ t('skills.noImportableHint') }}</p>
          </div>
        </div>

        <DialogFooter>
          <Button variant="ghost" @click="showImportExisting = false">{{ t('common.cancel') }}</Button>
          <Button :disabled="selectedPaths.length === 0 || importingUnmanaged" @click="confirmImportExisting">
            {{ importingUnmanaged ? t('skills.importing') : t('skills.importCount', { count: selectedPaths.length }) }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <!-- Agent 选择对话框（从 zip 安装） -->
    <Dialog v-model:open="showAgentSelector">
      <DialogContent class="max-w-md">
        <DialogHeader>
          <DialogTitle>{{ t('skills.selectTargetAgentsTitle') }}</DialogTitle>
          <DialogDescription>{{ t('skills.selectTargetAgentsDesc') }}</DialogDescription>
        </DialogHeader>
        <div class="space-y-2 py-2">
          <label class="flex items-center gap-2 text-sm font-medium">
            <input type="checkbox" :checked="selectedAgentIds.size === allCapableAgentIds.length" :indeterminate="selectedAgentIds.size > 0 && selectedAgentIds.size < allCapableAgentIds.length" @change="(e: Event) => toggleSelectAll((e.target as HTMLInputElement).checked)" class="h-4 w-4" />
            {{ t('common.toggleAll') }}
          </label>
          <div class="flex flex-wrap items-center gap-2 pt-1">
            <div v-for="group in skillCapableGroups" :key="group.id" class="flex items-center">
              <AgentToggleButton
                :agent-id="group.id"
                :agent-name="group.ids.length > 1 ? group.name : agentDisplayName({ name: group.name, id: group.id })"
                :model-value="group.ids.some(id => selectedAgentIds.has(id))"
                :disabled="group.status !== 'enabled'"
                :badge="group.ids.length > 1 ? null : variantToBadge(normalizeVariant(undefined, group.id))"
                @update:model-value="(v: boolean) => toggleGroupSelect(group, v)"
              />
            </div>
          </div>
        </div>
        <DialogFooter>
          <Button variant="ghost" @click="showAgentSelector = false">{{ t('common.cancel') }}</Button>
          <Button :disabled="selectedAgentIds.size === 0 || importingZip" @click="confirmZipImport">
            {{ importingZip ? t('skills.installing') : t('common.install') }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
