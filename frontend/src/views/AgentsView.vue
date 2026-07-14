<script setup lang="ts">
import { computed, ref } from 'vue'
import { useAgentsStore } from '@/stores/agents'
import { useMcpStore } from '@/stores/mcp'
import { Card, CardHeader, CardTitle, CardDescription, CardContent, Badge, Spinner, Button, Empty, EmptyHeader, EmptyTitle, EmptyDescription, EmptyMedia } from '@/components/ui'
import { PhArrowsClockwise, PhCheckCircle, PhXCircle, PhCircleNotch } from '@phosphor-icons/vue'
import { ApiError } from '@/lib/api'
import { agentLogoUrl, agentLogoInvertClass, statusVariant, statusLabel, variantLabel, getVariantFromId } from '@/composables/useAgentHelpers'
import { useToast } from '@/composables/useToast'

const agents = useAgentsStore()
const mcp = useMcpStore()
const toast = useToast()

const detected = computed(() => agents.items.filter((a) => a.status !== 'not_found').length)
const enabled = computed(() => agents.items.filter((a) => a.status === 'enabled').length)

async function onRescan() {
  try {
    await agents.rescan()
    toast.success(`已扫描完成，检测到 ${detected.value} 个 Agent`)
  } catch (e) {
    const err = ApiError.from(e)
    toast.error(`Rescan 失败：${err.message}`)
  }
}
</script>

<template>
  <div class="flex h-full flex-col">
    <!-- 固定头部 -->
    <div class="shrink-0 border-b border-border px-8 pt-8 pb-4">
      <div class="mx-auto max-w-6xl">
        <div class="flex items-end justify-between">
          <div>
            <h1 class="text-2xl font-semibold tracking-tight">Agent</h1>
            <p class="mt-1 text-sm text-muted-foreground">
              检测本机上的 AI Agent。在设置中启用或禁用 Agent 管理。
            </p>
          </div>
          <div class="flex items-center gap-2">
            <Button variant="default" size="sm" :disabled="agents.loading" @click="onRescan">
              <PhArrowsClockwise v-if="!agents.loading" :size="14" />
              <Spinner v-else class="size-3.5" />
              <span>重新扫描</span>
            </Button>
          </div>
        </div>

        <div class="mt-4 grid grid-cols-3 gap-3">
          <Card class="bg-card/50">
            <CardContent class="p-4">
              <div class="text-xs uppercase tracking-wider text-muted-foreground">已检测</div>
              <div class="mt-1 text-2xl font-semibold tabular-nums">{{ detected }}</div>
            </CardContent>
          </Card>
          <Card class="bg-card/50">
            <CardContent class="p-4">
              <div class="text-xs uppercase tracking-wider text-muted-foreground">已启用</div>
              <div class="mt-1 text-2xl font-semibold tabular-nums text-success">{{ enabled }}</div>
            </CardContent>
          </Card>
          <Card class="bg-card/50">
            <CardContent class="p-4">
              <div class="text-xs uppercase tracking-wider text-muted-foreground">MCP 总数</div>
              <div class="mt-1 text-2xl font-semibold tabular-nums">
                {{ mcp.total }}
              </div>
            </CardContent>
          </Card>
        </div>
      </div>
    </div>

    <!-- 可滚动列表 -->
    <div class="flex-1 overflow-y-auto">
      <div class="mx-auto max-w-6xl px-8 py-4">
        <div v-if="agents.loading && agents.items.length === 0" class="flex items-center justify-center py-16">
          <Spinner class="size-5" />
        </div>

        <Empty v-else-if="agents.items.length === 0" class="mt-8">
          <EmptyMedia><PhCircleNotch :size="32" class="text-muted-foreground" /></EmptyMedia>
          <EmptyHeader>
            <EmptyTitle>未检测到 Agent</EmptyTitle>
            <EmptyDescription>点击重新扫描来搜索系统。</EmptyDescription>
          </EmptyHeader>
        </Empty>

        <div v-else class="space-y-2">
          <Card
            v-for="agent in agents.sorted"
            :key="agent.id"
            :class="['transition-colors', agent.status === 'not_found' ? 'opacity-60' : '']"
          >
            <CardContent class="flex items-center gap-4 p-4">
              <div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-md bg-secondary p-2">
                <img :src="agentLogoUrl(agent.id)" :alt="agent.name" :class="['h-full w-full object-contain', agentLogoInvertClass(agent.id)]" />
              </div>
              <div class="min-w-0 flex-1">
                <div class="flex items-center gap-2">
                  <h3 class="text-sm font-semibold">{{ agent.name }}</h3>
                  <Badge variant="outline">{{ variantLabel(agent.variant || getVariantFromId(agent.id)) }}</Badge>
                  <Badge :variant="statusVariant(agent.status)">{{ statusLabel(agent.status) }}</Badge>
                </div>
                <p class="mt-0.5 truncate font-mono text-[11px] text-muted-foreground">
                  {{ agent.configPath || '无配置路径' }}
                </p>
                <div class="mt-2 flex items-center gap-3 text-xs text-muted-foreground">
                  <span class="flex items-center gap-1">
                    <PhCheckCircle v-if="agent.status !== 'not_found'" :size="11" weight="fill" class="text-success" />
                    <PhXCircle v-else :size="11" weight="fill" />
                    <span class="tabular-nums">{{ agent.mcpCount }}</span> MCP
                  </span>
                </div>
              </div>

            </CardContent>
          </Card>
        </div>

        <p v-if="agents.error" class="mt-4 text-xs text-destructive">
          {{ agents.error }}
        </p>

      </div>
    </div>
  </div>
</template>
