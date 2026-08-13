<script setup lang="ts">
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMcpStore } from '@/stores/mcp'
import { useAgentsStore } from '@/stores/agents'
import type { ScanItem } from '@/lib/api'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, Button, Badge, DialogFooter } from '@/components/ui'
import AgentToggleButton from '@/components/agent/AgentToggleButton.vue'
import { normalizeVariant, variantToBadge, agentDisplayName } from '@/composables/useAgentHelpers'

import { PhMagnifyingGlass, PhCheckCircle, PhWarningCircle } from '@phosphor-icons/vue'
import { useToast } from '@/composables/useToast'

const { t } = useI18n()
const mcp = useMcpStore()
const agents = useAgentsStore()
const toast = useToast()

const open = ref(false)
const scanning = ref(false)
const scanError = ref<string | null>(null)
const importing = ref(false)

const newItems = computed(() => (mcp.scanResult?.items ?? []).filter(i => !i.managed))

const enabledAgentGroups = computed(() =>
  agents.mergedGroups.filter(g => g.status === 'enabled')
)

// itemKey 仅作为 UI 状态的唯一键（勾选集合、v-for key），归一化语义以
// 后端 scanDedupKey 为准：后端 Scan 已按 URL 或归一化 command+args 合并条目，
// 前端不会同时出现两条同 key 的原始条目，此处直接用原始 command/args 即可。
// 不能用 server.name 做键——两个 agent 可能装了同名但命令不同的服务器，会互相覆盖。
function itemKey(item: ScanItem): string {
  const s = item.server
  if (s.transport === 'sse' || s.transport === 'http' || s.transport === 'streamable-http') {
    return `url:${s.url ?? ''}`
  }
  const cmd = s.command ?? ''
  return `cmd:${cmd}\x00${[cmd, ...(s.args ?? [])].join('\x00')}`
}

// 每个新发现服务器的导入配置：itemKey → 目标 agent ID 集合
// 默认不预选任何服务器/agent，全部由用户显式勾选
const importConfig = ref<Map<string, Set<string>>>(new Map())

const selectedKeys = computed(() => {
  const keys: string[] = []
  for (const [key, agentSet] of importConfig.value) {
    if (agentSet.size > 0) keys.push(key)
  }
  return keys
})

function itemByKey(key: string): ScanItem | undefined {
  return newItems.value.find(i => itemKey(i) === key)
}

function resolveSourceAgentIds(item: ScanItem): Set<string> {
  const ids = new Set<string>()
  // 同一 MCP 可能来自多个 agent 配置（如 Claude Code 和 OpenCode 都装了 context7），
  // 默认勾选所有来源 agent。按 configPath 精确匹配（共享配置文件的 agent 同路径）。
  // 来源 agent 全被禁用时不回退到无关 agent——留空由用户显式选择。
  // 组内可能混有 disabled 变体成员（合并组只聚合 status），须按成员级过滤，
  // 否则提交时后端 validateAgentIDs 会把 disabled 成员一并拒绝。
  for (const g of enabledAgentGroups.value) {
    const isSource = item.sources.some(s => s.configPath === g.configPath)
    if (isSource) {
      g.ids.forEach(id => { if (agents.activeIds.get(id)) ids.add(id) })
    }
  }
  return ids
}

async function handleScan() {
  if (scanning.value) return
  scanning.value = true
  scanError.value = null
  try {
    await mcp.scan()
    // 只发现、不管理：新发现的服务器默认不勾选，由用户显式勾选后再"加入管理"
    importConfig.value = new Map()
    open.value = true
  } catch {
    scanError.value = t('mcp.toast.scanFailed')
    open.value = true
  } finally {
    scanning.value = false
  }
}

function onOpenChange(isOpen: boolean) {
  open.value = isOpen
  if (!isOpen) {
    scanError.value = null
  }
}

function toggleSelect(key: string) {
  const next = new Map(importConfig.value)
  const existing = next.get(key)
  if (existing && existing.size > 0) {
    next.delete(key)
  } else {
    const item = itemByKey(key)
    const sourceAgentIds = item ? resolveSourceAgentIds(item) : new Set<string>()
    next.set(key, sourceAgentIds)
  }
  importConfig.value = next
}

// 组开关：对组内 active 成员整体 toggle。
// 合并组可能包含 disabled 变体成员，后端只接受 enabled/detected，须跳过 disabled。
function toggleGroupAgents(key: string, group: { ids: string[] }, enabled: boolean) {
  const next = new Map(importConfig.value)
  const current = next.get(key) ?? new Set<string>()
  const updated = new Set(current)
  for (const id of group.ids) {
    if (!agents.activeIds.get(id)) continue
    if (enabled) updated.add(id)
    else updated.delete(id)
  }
  if (updated.size === 0) {
    next.delete(key)
  } else {
    next.set(key, updated)
  }
  importConfig.value = next
}

function toggleSelectAll(checked: boolean) {
  if (checked) {
    const next = new Map<string, Set<string>>()
    for (const item of newItems.value) {
      const sourceAgentIds = resolveSourceAgentIds(item)
      next.set(itemKey(item), sourceAgentIds)
    }
    importConfig.value = next
  } else {
    importConfig.value = new Map()
  }
}

function onToggleSelectAll(e: Event) {
  toggleSelectAll((e.currentTarget as HTMLInputElement).checked)
}

function isSelected(key: string): boolean {
  const set = importConfig.value.get(key)
  return !!set && set.size > 0
}

function isGroupSelected(key: string, group: { ids: string[] }): boolean {
  const selected = importConfig.value.get(key)
  if (!selected) return false
  // disabled 变体成员不可选，不计入选中状态
  const active = group.ids.filter(id => agents.activeIds.get(id))
  return active.length > 0 && active.every(id => selected.has(id))
}

function isSourceItem(item: ScanItem, group: { ids: string[] }): boolean {
  // 检查该 group 中的任意 agent 是否为该 MCP 的来源之一
  return item.sources.some(s => group.ids.includes(s.agentId))
}

async function handleAdd() {
  if (selectedKeys.value.length === 0) return
  importing.value = true
  try {
    const results = await Promise.allSettled(
      selectedKeys.value.map(key => {
        const agentIDs = [...(importConfig.value.get(key) || [])]
        const item = itemByKey(key)
        if (!item || agentIDs.length === 0) return Promise.resolve()
        return mcp.add({ ...item.server, id: '' }, agentIDs)
      })
    )
    const failures = results.filter(r => r.status === 'rejected')
    const successCount = results.length - failures.length
    if (successCount > 0) {
      toast.success(t('mcp.toast.addedBatch', { count: successCount }))
    }
    if (failures.length > 0) {
      toast.warning(t('mcp.toast.batchAddFailed', { count: failures.length }))
    }
    await mcp.fetch()
    open.value = false
  } catch (e: unknown) {
    toast.error(toast.fromError(e, t('mcp.toast.addFailed')))
  } finally {
    importing.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open" @update:open="onOpenChange">
    <Button size="sm" variant="outline" :disabled="scanning" @click="handleScan">
      <PhMagnifyingGlass :size="14" :class="{ 'animate-pulse': scanning }" />
      <span>{{ scanning ? t('mcp.scanning') : t('mcp.scan') }}</span>
    </Button>

    <DialogContent class="max-w-2xl flex flex-col max-h-[85vh]">
      <DialogHeader>
        <DialogTitle>
          <template v-if="mcp.scanResult && newItems.length > 0">
            {{ t('mcp.scanFoundNew', { count: newItems.length }) }}
          </template>
          <template v-else>
            {{ t('mcp.scanResult') }}
          </template>
        </DialogTitle>
        <DialogDescription>
          <template v-if="mcp.scanResult && newItems.length > 0">
            {{ t('mcp.scanResultDesc') }}
          </template>
          <template v-else>
            {{ t('mcp.scanRedoDesc') }}
          </template>
        </DialogDescription>
      </DialogHeader>

      <div v-if="scanError" class="flex flex-col items-center justify-center py-12">
        <PhWarningCircle :size="32" class="text-destructive mb-3" />
        <p class="text-sm text-destructive">{{ scanError }}</p>
        <Button size="sm" variant="outline" class="mt-3" @click="handleScan">{{ t('common.retry') }}</Button>
      </div>

      <template v-else-if="mcp.scanResult">
        <div v-if="mcp.scanResult.failed > 0" class="flex items-center gap-2 rounded-md border border-amber-500/40 bg-amber-500/5 px-3 py-2 text-xs text-amber-600">
          <PhWarningCircle :size="14" class="shrink-0" />
          {{ t('mcp.scanFailedConfigs', { count: mcp.scanResult.failed }) }}
        </div>

        <div v-if="newItems.length === 0" class="flex flex-col items-center justify-center py-12 text-muted-foreground">
          <PhCheckCircle :size="40" class="text-emerald-500 mb-3" />
          <p class="text-sm font-medium">{{ t('mcp.allManaged') }}</p>
          <p class="text-xs mt-1">{{ t('mcp.noNewServers') }}</p>
        </div>

        <template v-else>
          <div class="flex flex-col gap-3 overflow-y-auto">
            <template v-if="newItems.length > 0">
              <div class="flex items-center justify-between border-b border-border pb-2">
                <label class="flex items-center gap-2 text-xs text-muted-foreground cursor-pointer">
                  <input
                    type="checkbox"
                    :checked="selectedKeys.length === newItems.length"
                    :indeterminate="selectedKeys.length > 0 && selectedKeys.length < newItems.length"
                    @change="onToggleSelectAll"
                    class="h-3.5 w-3.5"
                  />
                  {{ t('common.selectAll') }}
                </label>
                <span class="text-xs text-muted-foreground">{{ selectedKeys.length }} / {{ newItems.length }} {{ t('common.selected') }}</span>
              </div>

              <div class="max-h-[55vh] space-y-2 overflow-y-auto py-2 pr-1">
                <div
                  v-for="item in newItems"
                  :key="itemKey(item)"
                  class="rounded-md border p-3 transition-colors"
                  :class="isSelected(itemKey(item)) ? 'border-sky-500/40 bg-sky-500/5' : 'border-border'"
                >
                  <div class="flex items-start gap-3">
                    <input
                      type="checkbox"
                      :checked="isSelected(itemKey(item))"
                      @change="toggleSelect(itemKey(item))"
                      class="mt-0.5 h-4 w-4 shrink-0"
                    />
                    <div class="min-w-0 flex-1">
                      <div class="flex items-center gap-2">
                        <span class="text-sm font-semibold">{{ item.server.name }}</span>
                        <Badge variant="outline" class="border-sky-500/40 text-sky-600 text-[10px]">{{ t('mcp.badgeNew') }}</Badge>
                        <Badge variant="outline" class="text-[10px] font-mono">{{ item.server.transport }}</Badge>
                      </div>
                      <div v-if="item.server.command" class="mt-0.5 text-[11px] text-muted-foreground/70 font-mono">
                        {{ item.server.command }} {{ item.server.args?.join(' ') }}
                      </div>
                      <div v-if="item.server.url" class="mt-0.5 text-[11px] text-muted-foreground/70 font-mono">
                        {{ item.server.url }}
                      </div>
                      <p class="mt-0.5 text-[10px] text-muted-foreground/70">
                        <template v-if="item.sources.length > 1">{{ item.sources.map(s => s.agentName).join(' / ') }}</template>
                        <template v-else>{{ t('mcp.sourceLabel') }}: {{ item.sources[0]?.agentName }}</template>
                      </p>
                    </div>
                  </div>

                  <div class="mt-3 flex flex-wrap items-center gap-2 border-t border-border/50 pt-2 pl-7">
                    <div
                      v-for="group in enabledAgentGroups"
                      :key="group.id"
                      class="flex items-center gap-1.5"
                    >
                      <AgentToggleButton
                        :agent-id="group.id"
                        :agent-name="group.ids.length > 1 ? group.name : agentDisplayName({ name: group.name, id: group.id })"
                        :model-value="isGroupSelected(itemKey(item), group)"
                        :badge="group.ids.length > 1 ? null : variantToBadge(normalizeVariant(undefined, group.id))"
                        @update:model-value="(v: boolean) => toggleGroupAgents(itemKey(item), group, v)"
                      />
                      <Badge v-if="item.sources.length === 1 && isSourceItem(item, group)" variant="secondary" class="text-[9px] px-1 py-0">{{ t('mcp.sourceBadge') }}</Badge>
                    </div>
                  </div>
                </div>
              </div>
            </template>
          </div>

          <DialogFooter>
            <Button variant="ghost" @click="open = false">{{ t('common.cancel') }}</Button>
            <Button :disabled="selectedKeys.length === 0 || importing" @click="handleAdd">
              {{ importing ? t('mcp.adding') : t('mcp.addToAgentPack', { count: selectedKeys.length }) }}
            </Button>
          </DialogFooter>
        </template>
      </template>
    </DialogContent>
  </Dialog>
</template>