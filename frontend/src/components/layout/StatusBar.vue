<script setup lang="ts">
import { computed } from 'vue'
import { useAgentsStore } from '@/stores/agents'
import { useMcpStore } from '@/stores/mcp'
import { PhCircleNotch, PhCheckCircle, PhWarning } from '@phosphor-icons/vue'

const agents = useAgentsStore()
const mcp = useMcpStore()

const status = computed(() => {
  if (agents.loading) return 'loading'
  if (agents.error) return 'error'
  return 'ok'
})
</script>

<template>
  <footer
    class="flex h-7 shrink-0 items-center justify-between border-t border-border glass-surface px-3 text-[11px] text-muted-foreground"
  >
    <div class="flex items-center gap-3">
      <span class="flex items-center gap-1.5">
        <template v-if="status === 'loading'">
          <PhCircleNotch :size="11" class="animate-spin" />
          <span>加载中</span>
        </template>
        <template v-else-if="!agents.lastScanAt">
          <PhCircleNotch :size="11" class="animate-spin" />
          <span>正在检测...</span>
        </template>
        <template v-else-if="status === 'ok'">
          <PhCheckCircle :size="11" weight="fill" class="text-success" />
          <span>{{ agents.detected.length }} 个 Agents 已检测</span>
        </template>
        <template v-else>
          <PhWarning :size="11" weight="fill" class="text-destructive" />
          <span>错误</span>
        </template>
      </span>
      <span class="font-mono text-[10px] opacity-60">
        {{ mcp.total }} MCP
      </span>
    </div>
    <div class="flex items-center gap-3">
      <span v-if="agents.lastScanAt" class="font-mono text-[10px] opacity-60">
        上次扫描: {{ new Date(agents.lastScanAt).toLocaleTimeString() }}
      </span>
    </div>
  </footer>
</template>
