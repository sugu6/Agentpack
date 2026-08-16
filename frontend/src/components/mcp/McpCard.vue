<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAgentsStore } from '@/stores/agents'
import { useMcpStore } from '@/stores/mcp'
import { Card, CardContent, Badge, Button, AlertDialog, AlertDialogTrigger, AlertDialogContent, AlertDialogHeader, AlertDialogTitle, AlertDialogDescription, AlertDialogFooter, AlertDialogCancel, AlertDialogAction } from '@/components/ui'
import { PhTerminal, PhTrash } from '@phosphor-icons/vue'
import { normalizeVariant, variantToBadge, agentDisplayName } from '@/composables/useAgentHelpers'
import { transportLabel } from '@/lib/utils'
import AgentToggleButton from '@/components/agent/AgentToggleButton.vue'
import type { McpServer } from '@/lib/api'
import { ApiError } from '@/lib/api'
import { useToast } from '@/composables/useToast'
import McpForm from './McpForm.vue'

const props = defineProps<{ server: McpServer }>()
const emit = defineEmits<{
  (e: 'update'): void
}>()

const { t } = useI18n()
const agents = useAgentsStore()
const mcp = useMcpStore()
const toast = useToast()

const agentGroups = computed(() => agents.mergedGroups)

async function toggleGroup(group: { ids: string[]; id: string }, enabled: boolean) {
  // 串行提交而非 Promise.all：共享 configPath 的 agent（合并组）在后端
  // 各自 Update 同一配置文件，并行请求会产生中间态（前一个写完后
  // 广播 mcp:changed，卡片刷新读到未完成的绑定），串行保持一致性
  const failures: unknown[] = []
  for (const agentId of [...new Set(group.ids)]) {
    try {
      await mcp.toggleAgent(props.server.id, agentId, enabled)
    } catch (e) {
      failures.push(e)
    }
  }
  if (failures.length > 0) {
    const msg = failures.map((r) => (r as Error)?.message || t('mcp.toast.toggleFailed')).join('; ')
    toast.warning(t('mcp.toast.toggleBindingPartialFailed', { error: msg }))
  }
}

function isGroupBound(group: { ids: string[] }) {
  return group.ids.some(id => props.server.boundAgents?.includes(id))
}

async function handleRemove() {
  try {
    await mcp.remove(props.server.id)
    toast.success(t('mcp.toast.deleted', { name: props.server.name }))
    emit('update')
  } catch (e) {
    const apiError = ApiError.from(e)
    toast.error(t('mcp.toast.deleteFailed', { error: apiError.message }))
  }
}
</script>

<template>
  <Card>
    <CardContent class="p-4">
      <div class="flex items-start gap-4">
        <div class="flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary">
          <PhTerminal :size="16" weight="fill" />
        </div>
        <div class="min-w-0 flex-1">
          <div class="flex items-center gap-2">
            <h3 class="text-sm font-semibold">{{ server.name }}</h3>
            <Badge variant="outline">{{ transportLabel(server.transport) }}</Badge>
          </div>
          <p v-if="server.description" class="mt-0.5 text-xs text-muted-foreground">
            {{ server.description }}
          </p>
          <div class="mt-2 flex min-w-0 items-center gap-1.5">
            <code
              :title="`${server.command} ${(server.args || []).join(' ')}`"
              class="block max-w-full truncate rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-foreground"
            >
              {{ server.command }} {{ (server.args || []).join(' ') }}
            </code>
          </div>

          <div class="mt-3 flex flex-nowrap items-center gap-2 border-t border-border pt-3">
            <div
              v-for="group in agentGroups"
              :key="group.id"
              class="flex shrink-0 items-center"
            >
              <AgentToggleButton
                :agent-id="group.id"
                :agent-name="group.ids.length > 1 ? group.name : agentDisplayName({ name: group.name, id: group.id })"
                :model-value="isGroupBound(group)"
                :disabled="group.status !== 'enabled'"
                :badge="group.ids.length > 1 ? null : variantToBadge(normalizeVariant(undefined, group.id))"
                @update:model-value="(v: boolean) => toggleGroup(group, v)"
              />
            </div>
          </div>
        </div>
        <div class="flex shrink-0 gap-2">
          <McpForm mode="edit" :server="server" @updated="emit('update')" />
          <AlertDialog>
            <AlertDialogTrigger as-child>
              <Button variant="outline" size="icon" class="border-destructive/40 text-destructive hover:bg-destructive/10" :aria-label="t('common.delete')">
                <PhTrash :size="14" class="text-destructive" />
              </Button>
            </AlertDialogTrigger>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>{{ t('mcp.deleteConfirmTitle') }}</AlertDialogTitle>
                <AlertDialogDescription>
                  {{ t('mcp.deleteConfirmMessage', { name: server.name }) }}
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>{{ t('common.cancel') }}</AlertDialogCancel>
                <AlertDialogAction class="border border-destructive/40 bg-background text-destructive hover:bg-destructive/10" @click="handleRemove">{{ t('common.delete') }}</AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
        </div>
      </div>
    </CardContent>
  </Card>
</template>
