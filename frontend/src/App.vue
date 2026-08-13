<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import { useAgentsStore } from '@/stores/agents'
import { useSettingsStore } from '@/stores/settings'
import { useMcpStore } from '@/stores/mcp'
import { useSkillsStore } from '@/stores/skills'
import { api, events, type UpdateCheckResult, type SkillSourceBackfillResult } from '@/lib/api'
import { TooltipProvider, Toaster } from '@/components/ui'
import { useToast } from '@/composables/useToast'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import WindowCloseDialog from '@/components/WindowCloseDialog.vue'
import UpdateDialog from '@/components/UpdateDialog.vue'

const { t } = useI18n()
const toast = useToast()
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
let unsubscribeBackfill: (() => void) | undefined

// 自动来源回填的结果提示：事件与兜底查询都可能触发，只在有成功项时提示一次
let backfillNotified = false
function handleBackfillResult(res: unknown) {
  if (backfillNotified || !mounted.value) return
  const r = res as SkillSourceBackfillResult | null
  const matched = r?.matched?.length ?? 0
  if (matched === 0) return
  backfillNotified = true
  const skipped = (r?.mismatched?.length ?? 0) + (r?.unmatched?.length ?? 0)
  const failed = r?.failed?.length ?? 0
  toast.info(t('settings.toast.backfillSuccess', { count: matched, skipped, failed }), { duration: 5000 })
}

// 用户活动上报：节流后通知后端重置轻量模式空闲计时
const ACTIVITY_THROTTLE_MS = 30_000
const ACTIVITY_EVENTS = ['mousemove', 'mousedown', 'keydown', 'wheel', 'touchstart'] as const
let lastActivityReport = 0

function reportActivity() {
  const now = Date.now()
  if (now - lastActivityReport < ACTIVITY_THROTTLE_MS) return
  lastActivityReport = now
  api.system.notifyActivity().catch(() => {})
}

function addActivityListeners() {
  for (const name of ACTIVITY_EVENTS) {
    window.addEventListener(name, reportActivity, { passive: true })
  }
}

function removeActivityListeners() {
  for (const name of ACTIVITY_EVENTS) {
    window.removeEventListener(name, reportActivity)
  }
}

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
  addActivityListeners()
  // Register event listeners BEFORE async loads to avoid missing events
  unsubscribeAgentsChanged = events.on('agents:changed', handleAgentsChanged)
  unsubscribeMcpChanged = events.on('mcp:changed', handleMcpChanged)
  unsubscribeSkillsChanged = events.on('skills:changed', handleSkillsChanged)
  unsubscribeCloseRequested = events.on('app:close-requested', handleCloseRequested)
  // 自动来源回填结果（启动后后台执行；事件可能早于监听注册完成，用兜底查询补齐）
  unsubscribeBackfill = events.on('skills:backfill', handleBackfillResult)
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

  // 兜底查询自动来源回填结果（事件监听可能晚于回填完成；无结果时静默）
  api.skills.lastBackfillResult().then(handleBackfillResult).catch(() => {})

  // 首次启动时静默检查更新：使用 localStorage 记录检测状态，防止重复检测
  const UPDATE_CHECK_KEY = 'agentpack_update_checked_session'
  if (!localStorage.getItem(UPDATE_CHECK_KEY)) {
    localStorage.setItem(UPDATE_CHECK_KEY, '1')
    // 后台静默执行，不干扰用户操作
    api.system.checkUpdate().then((result: UpdateCheckResult) => {
      if (!mounted.value) return
      if (result.hasUpdate) {
        // 有更新时按现有提示机制处理（toast + UpdateDialog）
        toast.success(t('settings.toast.foundNewVersion', { latest: result.latestVersion, current: result.currentVersion }), {
          duration: 5000,
        })
        events.emit('app:update-available', result)
      }
      // 无更新时不显示任何提示，静默处理
    }).catch(() => {
      // 检测失败静默忽略，不打扰用户
    })
  }
})

onBeforeUnmount(() => {
  mounted.value = false
  removeActivityListeners()
  unsubscribeAgentsChanged?.()
  unsubscribeMcpChanged?.()
  unsubscribeSkillsChanged?.()
  unsubscribeCloseRequested?.()
  unsubscribeSettingsChanged?.()
  unsubscribeBackfill?.()
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
    <UpdateDialog />
    <Toaster position="top-center" :close-button="false" :theme="settings.theme" rich-colors />
  </TooltipProvider>
</template>
