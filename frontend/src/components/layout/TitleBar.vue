<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { PhPackage } from '@phosphor-icons/vue'

const route = useRoute()
const { t } = useI18n()
const title = computed(() => {
  switch (route.name as string) {
    case 'agents':
      return 'Agent'
    case 'mcp':
      return 'MCP Servers'
    case 'skills':
      return 'Skills'
    case 'market':
      return t('market.title')
    case 'settings':
      return t('settings.title')
    default:
      return 'AgentPack'
  }
})
</script>

<template>
  <header
    class="drag-region flex h-10 shrink-0 select-none items-center justify-between border-b border-border px-4"
  >
    <div class="flex items-center gap-2.5">
      <div class="flex h-6 w-6 items-center justify-center rounded-md bg-primary/10 text-primary">
        <PhPackage :size="14" weight="fill" />
      </div>
      <span class="text-sm font-medium tracking-tight">{{ title }}</span>
    </div>
  </header>
</template>

<style>
.drag-region {
  -webkit-app-region: drag;
}
.no-drag,
.no-drag * {
  -webkit-app-region: no-drag;
}
</style>
