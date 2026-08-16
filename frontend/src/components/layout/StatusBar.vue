<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAgentsStore } from '@/stores/agents'
import { PhCircleNotch, PhCheckCircle, PhWarning } from '@phosphor-icons/vue'

const { t } = useI18n()
const agents = useAgentsStore()

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
        <!-- 错误优先于"未扫描"：error 时若 lastScanAt 为空，
             原分支顺序会让错误被"检测中"永久遮蔽，用户无感知 -->
        <template v-else-if="status === 'error'">
          <PhWarning :size="11" weight="fill" class="text-destructive" />
          <span>{{ t('common.error') }}</span>
        </template>
        <template v-else-if="!agents.lastScanAt">
          <PhCircleNotch :size="11" class="animate-spin" />
          <span>{{ t('status.detecting') }}</span>
        </template>
        <template v-else-if="status === 'ok'">
          <PhCheckCircle :size="11" weight="fill" class="text-success" />
          <span>{{ t('status.agentsDetected', { count: agents.detected.length }) }}</span>
        </template>
      </span>
    </div>
    <div class="flex items-center gap-3">
      <span v-if="agents.lastScanAt" class="font-mono text-[10px] opacity-60">
        {{ t('status.lastScan', { time: new Date(agents.lastScanAt).toLocaleTimeString() }) }}
      </span>
    </div>
  </footer>
</template>
