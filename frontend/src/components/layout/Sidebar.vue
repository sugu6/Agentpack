<script setup lang="ts">
import { computed } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { PhRobot, PhPlugs, PhSparkle, PhStorefront, PhGearSix } from '@phosphor-icons/vue'
import { useAgentsStore } from '@/stores/agents'
import { useMcpStore } from '@/stores/mcp'
import { useSettingsStore } from '@/stores/settings'

const router = useRouter()
const route = useRoute()
const agents = useAgentsStore()
const mcp = useMcpStore()
const settings = useSettingsStore()

const nav = computed(() => [
  { to: '/', label: 'Agents', icon: PhRobot, badge: agents.detected.length },
  { to: '/mcp', label: 'MCP', icon: PhPlugs, badge: mcp.total },
  { to: '/skills', label: 'Skills', icon: PhSparkle },
  { to: '/market', label: 'Market', icon: PhStorefront },
  { to: '/settings', label: '设置', icon: PhGearSix },
])

const skillsDir = computed(() =>
  settings.config.skillStorage === 'unified' ? '~/.agents/skills' : '~/.agentpack/skills'
)

function handleNav(to: string) {
  router.push(to).catch(() => {})
}

function isActive(to: string) {
  if (to === '/') return route.path === '/'
  return route.path.startsWith(to)
}
</script>

<template>
  <aside class="flex w-56 shrink-0 flex-col border-r border-border sidebar-surface">
    <nav class="flex-1 space-y-0.5 p-2">
      <button
        v-for="item in nav"
        :key="item.to"
        @click="handleNav(item.to)"
        class="group flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-sm font-medium transition-colors"
        :class="
          isActive(item.to)
            ? 'bg-primary/10 text-primary'
            : 'text-muted-foreground hover:bg-accent hover:text-foreground'
        "
      >
        <component :is="item.icon" :size="16" :weight="isActive(item.to) ? 'fill' : 'regular'" />
        <span class="flex-1 text-left">{{ item.label }}</span>
        <span
          v-if="item.badge !== undefined && item.badge > 0"
          class="rounded-full bg-muted px-1.5 py-0.5 text-[10px] font-mono tabular-nums text-muted-foreground"
        >
          {{ item.badge }}
        </span>
      </button>
    </nav>
    <div class="border-t border-border p-3 text-[10px] leading-relaxed text-muted-foreground">
      <p class="font-mono">{{ skillsDir }}</p>
    </div>
  </aside>
</template>
