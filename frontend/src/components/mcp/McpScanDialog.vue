<script setup lang="ts">
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMcpStore } from '@/stores/mcp'
import { useAgentsStore } from '@/stores/agents'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, Button, Badge, DialogFooter } from '@/components/ui'
import AgentToggleButton from '@/components/agent/AgentToggleButton.vue'
import { getVariantFromId, variantToBadge } from '@/composables/useAgentHelpers'
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

// 每个新发现服务器的导入配置：服务器名称 → 目标 agent ID 集合
// 默认只启用来源 agent
const importConfig = ref<Map<string, Set<string>>>(new Map())

const selectedNames = computed(() => {
  const names: string[] = []
  for (const [name, agentSet] of importConfig.value) {
    if (agentSet.size > 0) names.push(name)
  }
  return names
})

function resolveSourceAgentIds(item: { configPath: string }): Set<string> {
  const ids = new Set<string>()
  for (const g of enabledAgentGroups.value) {
    if (item.configPath.startsWith(g.configPath) || g.configPath.startsWith(item.configPath)) {
      g.ids.forEach(id => ids.add(id))
    }
  }
  if (ids.size === 0) {
    enabledAgentGroups.value[0]?.ids.forEach(id => ids.add(id))
  }
  return ids
}

async function handleScan() {
  if (scanning.value) return
  scanning.value = true
  scanError.value = null
  try {
    await mcp.scan()
    importConfig.value = new Map()
    for (const item of newItems.value) {
      const sourceAgentIds = resolveSourceAgentIds(item)
      importConfig.value.set(item.server.name, sourceAgentIds)
    }
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

function toggleSelect(name: string) {
  const next = new Map(importConfig.value)
  const existing = next.get(name)
  if (existing && existing.size > 0) {
    next.delete(name)
  } else {
    const item = newItems.value.find(i => i.server.name === name)
    const sourceAgentIds = item ? resolveSourceAgentIds(item) : new Set<string>()
    if (sourceAgentIds.size === 0) {
      enabledAgentGroups.value[0]?.ids.forEach(id => sourceAgentIds.add(id))
    }
    next.set(name, sourceAgentIds)
  }
  importConfig.value = next
}

function toggleAgent(name: string, agentId: string, enabled: boolean) {
  const next = new Map(importConfig.value)
  let current = next.get(name)
  if (!current) {
    current = new Set()
    next.set(name, current)
  }
  const updated = new Set(current)
  if (enabled) {
    updated.add(agentId)
  } else {
    updated.delete(agentId)
  }
  if (updated.size === 0) {
    next.delete(name)
  } else {
    next.set(name, updated)
  }
  importConfig.value = next
}

function toggleSelectAll(checked: boolean) {
  if (checked) {
    const next = new Map<string, Set<string>>()
    for (const item of newItems.value) {
      const sourceAgentIds = resolveSourceAgentIds(item)
      next.set(item.server.name, sourceAgentIds)
    }
    importConfig.value = next
  } else {
    importConfig.value = new Map()
  }
}

function onToggleSelectAll(e: Event) {
  toggleSelectAll((e.currentTarget as HTMLInputElement).checked)
}

function isSelected(name: string): boolean {
  const set = importConfig.value.get(name)
  return !!set && set.size > 0
}

function isGroupSelected(name: string, group: { ids: string[] }): boolean {
  const selected = importConfig.value.get(name)
  if (!selected) return false
  return group.ids.some(id => selected.has(id))
}

function isSourceAgent(name: string, group: { id: string }): boolean {
  const item = newItems.value.find(i => i.server.name === name)
  if (!item) return false
  return item.agentId === group.id
}

async function handleAdd() {
  if (selectedNames.value.length === 0) return
  importing.value = true
  try {
    const results = await Promise.allSettled(
      selectedNames.value.map(name => {
        const agentIDs = [...(importConfig.value.get(name) || [])]
        const item = newItems.value.find(i => i.server.name === name)
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
        <div v-if="newItems.length === 0" class="flex flex-col items-center justify-center py-12 text-muted-foreground">
          <PhCheckCircle :size="40" class="text-emerald-500 mb-3" />
          <p class="text-sm font-medium">{{ t('mcp.allManaged') }}</p>
          <p class="text-xs mt-1">{{ t('mcp.noNewServers') }}</p>
        </div>

        <template v-else>
          <div class="flex items-center justify-between border-b border-border pb-2">
            <label class="flex items-center gap-2 text-xs text-muted-foreground cursor-pointer">
              <input
                type="checkbox"
                :checked="selectedNames.length === newItems.length"
                :indeterminate="selectedNames.length > 0 && selectedNames.length < newItems.length"
                @change="onToggleSelectAll"
                class="h-3.5 w-3.5"
              />
              {{ t('common.selectAll') }}
            </label>
            <span class="text-xs text-muted-foreground">{{ selectedNames.length }} / {{ newItems.length }} {{ t('common.selected') }}</span>
          </div>

          <div class="max-h-[55vh] space-y-2 overflow-y-auto py-2 pr-1">
            <div
              v-for="item in newItems"
              :key="item.server.name"
              class="rounded-md border p-3 transition-colors"
              :class="isSelected(item.server.name) ? 'border-sky-500/40 bg-sky-500/5' : 'border-border'"
            >
              <div class="flex items-start gap-3">
                <input
                  type="checkbox"
                  :checked="isSelected(item.server.name)"
                  @change="toggleSelect(item.server.name)"
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
                    {{ t('mcp.sourceLabel') }}: {{ item.agentName }} · {{ item.configPath }}
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
                    :agent-name="group.name"
                    :model-value="isGroupSelected(item.server.name, group)"
                    :badge="variantToBadge(getVariantFromId(group.id))"
                    @update:model-value="(v: boolean) => toggleAgent(item.server.name, group.id, v)"
                  />
                  <Badge v-if="isSourceAgent(item.server.name, group)" variant="secondary" class="text-[9px] px-1 py-0">{{ t('mcp.sourceBadge') }}</Badge>
                </div>
              </div>
            </div>
          </div>

          <DialogFooter>
            <Button variant="ghost" @click="open = false">{{ t('common.cancel') }}</Button>
            <Button :disabled="selectedNames.length === 0 || importing" @click="handleAdd">
              {{ importing ? t('mcp.adding') : t('mcp.addToAgentPack', { count: selectedNames.length }) }}
            </Button>
          </DialogFooter>
        </template>
      </template>
    </DialogContent>
  </Dialog>
</template>