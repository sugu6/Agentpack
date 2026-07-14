<script setup lang="ts">
import { computed } from 'vue'
import { useMcpStore } from '@/stores/mcp'
import { useAgentsStore } from '@/stores/agents'
import { Card, CardContent, Spinner, Empty, EmptyHeader, EmptyTitle, EmptyDescription, EmptyMedia } from '@/components/ui'
import { PhPlugsConnected } from '@phosphor-icons/vue'
import McpCard from '@/components/mcp/McpCard.vue'
import McpForm from '@/components/mcp/McpForm.vue'
import McpScanDialog from '@/components/mcp/McpScanDialog.vue'
import { ApiError } from '@/lib/api'

const mcp = useMcpStore()
const agents = useAgentsStore()

const search = computed(() => mcp.items)

async function handleUpdated() {
  try {
    await mcp.fetch()
  } catch (e) {
    const apiError = ApiError.from(e)
    console.warn('刷新 MCP 服务器失败:', apiError.message)
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
            <h1 class="text-2xl font-semibold tracking-tight">MCP 服务器</h1>
            <p class="mt-1 text-sm text-muted-foreground">
              模型上下文协议服务器。一次定义，所有 Agent 通用。
            </p>
          </div>
          <div class="flex items-center gap-2">
            <McpScanDialog />
            <McpForm mode="add" />
          </div>
        </div>

        <div class="mt-4 grid grid-cols-2 gap-3">
          <Card class="bg-card/50">
            <CardContent class="p-4">
              <div class="text-xs uppercase tracking-wider text-muted-foreground">已安装</div>
              <div class="mt-1 text-2xl font-semibold tabular-nums">{{ mcp.total }}</div>
            </CardContent>
          </Card>
          <Card class="bg-card/50">
            <CardContent class="p-4">
              <div class="text-xs uppercase tracking-wider text-muted-foreground">活跃 Agent</div>
              <div class="mt-1 text-2xl font-semibold tabular-nums">{{ agents.active.length }}</div>
            </CardContent>
          </Card>
        </div>
      </div>
    </div>

    <!-- 可滚动列表 -->
    <div class="flex-1 overflow-y-auto">
      <div class="mx-auto max-w-6xl px-8 py-4">
        <div v-if="mcp.loading && mcp.items.length === 0" class="flex items-center justify-center py-16">
          <Spinner class="size-5" />
        </div>

        <Empty v-else-if="mcp.items.length === 0" class="mt-4">
          <EmptyMedia><PhPlugsConnected :size="32" class="text-muted-foreground" /></EmptyMedia>
          <EmptyHeader>
            <EmptyTitle>未安装 MCP 服务器</EmptyTitle>
            <EmptyDescription>点击右上角添加自定义服务器。</EmptyDescription>
          </EmptyHeader>
        </Empty>

        <div v-else class="space-y-2">
          <div v-for="server in mcp.items" :key="server.id">
            <McpCard
              :server="server"
              @update="handleUpdated"
            />
          </div>
        </div>

        <p v-if="mcp.error" class="mt-4 text-xs text-destructive">
          {{ mcp.error }}
        </p>

      </div>
    </div>
  </div>
</template>
