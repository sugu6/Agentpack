<script setup lang="ts">
import { computed } from 'vue'
import { useAgentsStore } from '@/stores/agents'
import { useMcpStore } from '@/stores/mcp'
import { Card, CardContent, Badge, Button, AlertDialog, AlertDialogTrigger, AlertDialogContent, AlertDialogHeader, AlertDialogTitle, AlertDialogDescription, AlertDialogFooter, AlertDialogCancel, AlertDialogAction } from '@/components/ui'
import { PhTerminal, PhTrash } from '@phosphor-icons/vue'
import { getVariantFromId, variantLabel, variantToBadge } from '@/composables/useAgentHelpers'
import AgentToggleButton from '@/components/agent/AgentToggleButton.vue'
import type { McpServer } from '@/lib/api'
import { ApiError } from '@/lib/api'
import { useToast } from '@/composables/useToast'
import McpForm from './McpForm.vue'

const props = defineProps<{ server: McpServer }>()
const emit = defineEmits<{
  (e: 'update'): void
}>()

const agents = useAgentsStore()
const mcp = useMcpStore()
const toast = useToast()

const agentGroups = computed(() => agents.mergedGroups)

async function toggleGroup(group: { ids: string[]; id: string }, enabled: boolean) {
  try {
    const uniqueIds = [...new Set(group.ids)]
    const results = await Promise.allSettled(
      uniqueIds.map((agentId) => mcp.toggleAgent(props.server.id, agentId, enabled))
    )
    const failures = results.filter((r) => r.status === 'rejected')
    if (failures.length > 0) {
      const msg = failures.map((r) => (r as PromiseRejectedResult).reason?.message || '切换失败').join('; ')
      toast.warning(`部分 Agent 绑定切换失败：${msg}`)
    }
  } catch (e) {
    const apiError = ApiError.from(e)
    toast.error(`切换 Agent 绑定失败：${apiError.message}`)
  }
}

function isGroupBound(group: { ids: string[] }) {
  return group.ids.some(id => props.server.boundAgents?.includes(id))
}

function transportLabel() {
  if (props.server.transport === 'sse') return 'SSE'
  if (props.server.transport === 'http') return 'HTTP'
  return 'stdio'
}

async function handleRemove() {
  try {
    await mcp.remove(props.server.id)
    toast.success(`已删除 MCP 服务器 ${props.server.name}`)
    emit('update')
  } catch (e) {
    const apiError = ApiError.from(e)
    toast.error(`删除失败：${apiError.message}`)
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
            <Badge variant="outline">{{ transportLabel() }}</Badge>
          </div>
          <p v-if="server.description" class="mt-0.5 text-xs text-muted-foreground">
            {{ server.description }}
          </p>
          <div class="mt-2 flex items-center gap-1.5">
            <code class="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-foreground">
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
                :agent-name="`${group.name} (${variantLabel(getVariantFromId(group.id))})`"
                :model-value="isGroupBound(group)"
                :disabled="group.status !== 'enabled'"
                :badge="variantToBadge(getVariantFromId(group.id))"
                @update:model-value="(v: boolean) => toggleGroup(group, v)"
              />
            </div>
          </div>
        </div>
        <div class="flex shrink-0 gap-2">
          <McpForm mode="edit" :server="server" @updated="emit('update')" />
          <AlertDialog>
            <AlertDialogTrigger as-child>
              <Button variant="outline" size="icon" class="border-destructive/40 text-destructive hover:bg-destructive/10" aria-label="删除">
                <PhTrash :size="14" class="text-destructive" />
              </Button>
            </AlertDialogTrigger>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>确认删除</AlertDialogTitle>
                <AlertDialogDescription>
                  确定要删除 MCP 服务器 "{{ server.name }}" 吗？将从所有绑定的 Agent 配置中移除。
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>取消</AlertDialogCancel>
                <AlertDialogAction class="border border-destructive/40 bg-background text-destructive hover:bg-destructive/10" @click="handleRemove">删除</AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
        </div>
      </div>
    </CardContent>
  </Card>
</template>
