<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAgentsStore } from '@/stores/agents'
import { useMcpStore } from '@/stores/mcp'
import { PhCircleNotch, PhCheckCircle, PhWarning } from '@phosphor-icons/vue'

const { t } = useI18n()
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
          <span>{{ t('common.loading') }}</span>
        </template>
        <template v-else-if="!agents.lastScanAt">
          <PhCircleNotch :size="11" class="animate-spin" />
          <span>{{ t('status.detecting') }}</span>
        </template>
        <template v-else-if="status === 'ok'">
          <PhCheckCircle :size="11" weight="fill" class="text-success" />
          <span>{{ t('status.agentsDetected', { count: agents.detected.length }) }}</span>
        </template>
        <template v-else>
          <PhWarning :size="11" weight="fill" class="text-destructive" />
          <span>{{ t('common.error') }}</span>
        </template>
      </span>
      <span class="font-mono text-[10px] opacity-60">
        {{ mcp.total }} MCP
      </span>
    </div>
    <div class="flex items-center gap-3">
      <span v-if="agents.lastScanAt" class="font-mono text-[10px] opacity-60">
        {{ t('status.lastScan', { time: new Date(agents.lastScanAt).toLocaleTimeString() }) }}
      </span>
    </div>
  </footer>
</template>
