<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import { useAgentsStore } from '@/stores/agents'
import { useSettingsStore } from '@/stores/settings'
import { useMcpStore } from '@/stores/mcp'
import { useSkillsStore } from '@/stores/skills'
import { api, events } from '@/lib/api'
import { TooltipProvider, Toaster } from '@/components/ui'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import WindowCloseDialog from '@/components/WindowCloseDialog.vue'

const { t } = useI18n()
const agents = useAgentsStore()
const settings = useSettingsStore()
const mcp = useMcpStore()
const skills = useSkillsStore()

const mounted = ref(false)
const startupErrors = ref<string[]>([])
const closeDialogOpen = ref(false)
let unsubscribeAgentsChanged: (() => void) | undefined
let unsubscribeMcpChanged: (() => void) | undefined
let unsubscribeSkillsChanged: (() => void) | undefined
let unsubscribeCloseRequested: (() => void) | undefined
let unsubscribeSettingsChanged: (() => void) | undefined

function handleAgentsChanged() {
  if (!mounted.value) return
  agents.fetch().catch((e) => console.warn('刷新 Agent 列表失败:', e))
}

function handleMcpChanged() {
  if (!mounted.value) return
  mcp.fetch().catch((e) => console.warn('刷新 MCP 列表失败:', e))
}

function handleSkillsChanged() {
  if (!mounted.value) return
  skills.load().catch((e) => console.warn('刷新 Skills 列表失败:', e))
}

async function handleCloseRequested() {
  if (!mounted.value) return
  try {
    const noRemind = settings.config.windowNoRemind ?? false
    if (noRemind) {
      const action = settings.config.windowAction || 'minimize'
      if (action === 'exit') {
        await api.system.quit()
      } else {
        await api.system.hideWindow()
      }
    } else {
      closeDialogOpen.value = true
    }
  } catch (e) {
    console.warn('处理关闭请求失败:', e)
  }
}

onMounted(async () => {
  mounted.value = true
  // Register event listeners BEFORE async loads to avoid missing events
  unsubscribeAgentsChanged = events.on('agents:changed', handleAgentsChanged)
  unsubscribeMcpChanged = events.on('mcp:changed', handleMcpChanged)
  unsubscribeSkillsChanged = events.on('skills:changed', handleSkillsChanged)
  unsubscribeCloseRequested = events.on('app:close-requested', handleCloseRequested)
  // 全局 settings:changed 监听：SettingsView 未挂载时（如在市场/技能页操作）也能同步设置 store
  unsubscribeSettingsChanged = events.on('settings:changed', () => {
    if (!mounted.value) return
    settings.fetch().catch((e) => console.warn('刷新设置失败:', e))
  })

  const results = await Promise.allSettled([
    settings.fetch(),
    agents.fetch(),
    mcp.fetch(),
    skills.load(),
  ])
  for (const r of results) {
    if (r.status === 'rejected') {
      console.warn('启动数据加载失败:', r.reason?.message)
    }
  }

  api.system.getStartupErrors().then((errs) => {
    if (!mounted.value) return
    startupErrors.value = errs || []
  }).catch((e) => console.warn('读取启动错误失败:', e))
})

onBeforeUnmount(() => {
  mounted.value = false
  unsubscribeAgentsChanged?.()
  unsubscribeMcpChanged?.()
  unsubscribeSkillsChanged?.()
  unsubscribeCloseRequested?.()
  unsubscribeSettingsChanged?.()
  settings.dispose()
})
</script>

<template>
  <div v-if="startupErrors.length > 0" class="bg-destructive/10 border-b border-destructive/30 px-4 py-2">
    <div class="mx-auto max-w-6xl">
      <p class="text-sm font-medium text-destructive">{{ t('startup.errors') }}</p>
      <ul class="mt-1 list-inside list-disc text-xs text-destructive/80">
        <li v-for="(err, i) in startupErrors" :key="i">{{ err }}</li>
      </ul>
    </div>
  </div>
  <TooltipProvider>
    <AppLayout />
    <ConfirmDialog />
    <WindowCloseDialog v-model:open="closeDialogOpen" />
    <Toaster position="top-center" :close-button="false" :theme="settings.theme" rich-colors />
  </TooltipProvider>
</template>
