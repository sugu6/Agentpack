<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useSettingsStore } from '@/stores/settings'
import { useAgentsStore } from '@/stores/agents'
import { Card, CardContent, CardHeader, CardTitle, CardDescription, Switch, Button, Separator, Input, Label, Tabs, TabsList, TabsTrigger, Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter, Checkbox, RadioGroup, RadioGroupItem } from '@/components/ui'
import AgentToggleButton from '@/components/agent/AgentToggleButton.vue'
import { PhFolderOpen, PhArrowsClockwise, PhDownload, PhUpload, PhPlus, PhTrash, PhPencilSimple } from '@phosphor-icons/vue'
import { api, ApiError, events, type SkillRepo, type UpdateCheckResult } from '@/lib/api'
import { getVariantFromId, variantToBadge, agentDisplayName } from '@/composables/useAgentHelpers'
import { useToast } from '@/composables/useToast'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import { useI18n } from 'vue-i18n'

// 将 changelog markdown 渲染为安全的 HTML
function renderMarkdown(md: string): string {
  if (!md) return ''
  return DOMPurify.sanitize(marked.parse(md, { async: false }) as string)
}

const settings = useSettingsStore()
const agents = useAgentsStore()
const toast = useToast()
const { t } = useI18n()

const saving = ref(false)
const backupLoading = ref<'create' | 'export' | 'import' | null>(null)
const updateChecking = ref(false)
const updateResult = ref<UpdateCheckResult | null>(null)
const changelogDialogOpen = ref(false)
let saveDirty = false

// 版本号（从后端 API 获取）
const appVersion = ref('')

// 下载状态
const downloadStatus = ref<'idle' | 'downloading' | 'complete' | 'error'>('idle')
const downloadProgress = ref(0)
const downloadSpeed = ref('')
const downloadedFilePath = ref('')
const downloadedFileName = ref('')
let offDownloadProgress: (() => void) | null = null
let offDownloadComplete: (() => void) | null = null
let offDownloadError: (() => void) | null = null

// 导入确认弹窗状态
const importDialog = ref({
  open: false,
  filePath: '',
  overwrite: false,
  applyAgentStatus: true,
  applySettings: false,
})

// 监听 settings:changed 事件，导入设置后刷新前端状态
let offSettingsChanged: (() => void) | null = null
onMounted(() => {
  offSettingsChanged = events.on('settings:changed', () => {
    void settings.fetch()
  })
  // 主动拉取一次设置，防止 store 在其他页面操作后陈旧
  void settings.fetch()
  // 从后端获取版本号
  api.system.getAppVersion().then(v => { appVersion.value = v }).catch(() => {})
  // 监听下载进度事件
  offDownloadProgress = events.on('update:download:progress', (data: any) => {
    if (data && typeof data === 'object') {
      downloadProgress.value = Math.round(data.percent || 0)
      downloadSpeed.value = formatSpeed(data.speed)
    }
  })
  offDownloadComplete = events.on('update:download:complete', (data: any) => {
    if (data && typeof data === 'object') {
      downloadStatus.value = 'complete'
      downloadedFilePath.value = data.filePath || ''
      downloadedFileName.value = data.fileName || ''
    }
  })
  offDownloadError = events.on('update:download:error', (data: any) => {
    if (data && typeof data === 'object') {
      downloadStatus.value = 'error'
      downloadProgress.value = 0
      downloadSpeed.value = ''
      toast.error(t('settings.toast.downloadFailedMsg', { message: data.message || t('settings.toast.unknownError') }))
    }
  })
})
onUnmounted(() => {
  if (offSettingsChanged) offSettingsChanged()
  if (offDownloadProgress) offDownloadProgress()
  if (offDownloadComplete) offDownloadComplete()
  if (offDownloadError) offDownloadError()
})

const MIN_RETENTION = 1
const MAX_RETENTION = 100

// Skills 仓库扫描表单
// 支持 "owner/name" 或 "https://github.com/owner/name[/tree/branch]"
const newRepo = ref<{ url: string; branch: string }>({ url: '', branch: '' })
const repoBusy = ref(false)
const repoError = ref<string | null>(null)

// 编辑态:editingRepo 指向当前正在编辑的原条目(null 表示非编辑态)
const editingRepo = ref<SkillRepo | null>(null)
const editForm = ref<{ url: string; branch: string }>({ url: '', branch: '' })

function cloneConfig() {
  return JSON.parse(JSON.stringify(settings.config))
}

async function autoSave(previous?: ReturnType<typeof cloneConfig>) {
  if (saving.value) {
    // Queue the save instead of dropping it
    saveDirty = true
    return
  }
  saving.value = true
  try {
    await settings.update(cloneConfig())
  } catch (e: unknown) {
    if (previous) {
      settings.setConfig(previous)
      settings.applyTheme(previous.theme)
    }
    toast.error(toast.fromError(e, t('toast.saveFailed')))
  } finally {
    saving.value = false
  }
  // Re-save if changes occurred during the in-flight save
  if (saveDirty) {
    saveDirty = false
    await autoSave()
  }
}

function withAutoSave(mutate: (cfg: typeof settings.config) => void) {
  const previous = cloneConfig()
  mutate(settings.config)
  void autoSave(previous)
}

function setTheme(t: 'light' | 'dark' | 'system') {
  withAutoSave(cfg => {
    cfg.theme = t
    settings.applyTheme(t)
  })
}

function setLanguage(v: string) {
  withAutoSave(cfg => { cfg.language = v })
}

function setWindowAction(v: string) {
  if (v !== 'minimize' && v !== 'exit') return
  withAutoSave(cfg => { cfg.windowAction = v as 'minimize' | 'exit' })
}

function setWindowNoRemind(v: boolean) {
  withAutoSave(cfg => { cfg.windowNoRemind = v })
}

function setMarketSource(key: string, v: boolean) {
  withAutoSave(cfg => {
    const marketSources = cfg.marketSources
    if (marketSources && key in marketSources) {
      marketSources[key].enabled = v
    }
  })
}

function setAutoBackup(v: boolean) {
  withAutoSave(cfg => { cfg.autoBackup = v })
}

function setSkillStorage(v: string) {
  if (v !== 'agentpack' && v !== 'unified') return
  withAutoSave(cfg => { cfg.skillStorage = v as 'agentpack' | 'unified' })
}

function setSkillSyncMethod(v: string) {
  if (v !== 'symlink' && v !== 'copy') return
  withAutoSave(cfg => { cfg.skillSyncMethod = v as 'symlink' | 'copy' })
}

function setBackupRetention(v: string | number) {
  const parsed = Number(v)
  const value = Number.isFinite(parsed) ? Math.min(Math.max(Math.trunc(parsed), MIN_RETENTION), MAX_RETENTION) : MIN_RETENTION
  withAutoSave(cfg => {
    cfg.backupRetention = value
    cfg.backupCount = value
  })
}

async function toggleAgent(agentId: string, enabled: boolean) {
  try {
    await agents.toggle(agentId, enabled)
    toast.success(enabled ? t('settings.toast.agentEnabled') : t('settings.toast.agentDisabled'))
  } catch (e) {
    const apiError = ApiError.from(e)
    toast.error(t('settings.toast.toggleAgentFailed', { error: apiError.message }))
  }
}

async function createBackup() {
  backupLoading.value = 'create'
  try {
    await api.backup.create(t('settings.toast.manualBackupLabel'))
    toast.success(t('settings.toast.backupCreated'))
  } catch (e) {
    const apiError = ApiError.from(e)
    toast.error(t('settings.toast.backupFailed', { error: apiError.message }))
  } finally {
    backupLoading.value = null
  }
}

async function exportData() {
  backupLoading.value = 'export'
  try {
    const exportDir = await api.system.pickDirectory()
    if (!exportDir) return
    const summary = await api.backup.create(t('settings.toast.exportBackupLabel'))
    const backupId = summary.id || ''
    if (!backupId) {
      toast.error(t('settings.toast.createBackupFailedNoId'))
      return
    }
    const dest = `${exportDir}/agentpack-export-${Date.now()}.json`
    await api.export.exportData(backupId, dest)
    toast.success(t('settings.toast.exportSuccess', { dest }))
  } catch (e) {
    const apiError = ApiError.from(e)
    toast.error(t('settings.toast.exportFailed', { error: apiError.message }))
  } finally {
    backupLoading.value = null
  }
}

async function importData() {
  try {
    const path = await api.system.pickFile('.json')
    if (!path) return
    // 打开确认弹窗
    importDialog.value = {
      open: true,
      filePath: path,
      overwrite: false,
      applyAgentStatus: true,
      applySettings: false,
    }
  } catch (e) {
    const apiError = ApiError.from(e)
    toast.error(t('settings.toast.pickFileFailed', { error: apiError.message }))
  }
}

async function confirmImport() {
  const { filePath, overwrite, applyAgentStatus, applySettings } = importDialog.value
  importDialog.value.open = false
  backupLoading.value = 'import'
  try {
    const result = await api.export.importData(filePath, { overwrite, applyAgentStatus, applySettings })
    const parts: string[] = []
    if (result.mcpApplied > 0) parts.push(t('settings.toast.importedServers', { count: result.mcpApplied }))
    if (result.mcpSkipped > 0) parts.push(t('settings.toast.importedSkipped', { count: result.mcpSkipped }))
    if (result.agentStatusApplied > 0) parts.push(t('settings.toast.agentStatusRestored', { count: result.agentStatusApplied }))
    if (applySettings && result.exportedSettings) parts.push(t('settings.toast.appSettingsRestored'))
    toast.success(parts.length > 0 ? t('settings.toast.importCompleteWithDetails', { details: parts.join(t('settings.toast.detailsSeparator')) }) : t('settings.toast.importComplete'))
  } catch (e) {
    const apiError = ApiError.from(e)
    toast.error(t('settings.toast.importFailed', { error: apiError.message }))
  } finally {
    backupLoading.value = null
  }
}

async function openConfigFolder() {
  try {
    await api.system.openConfigFolder()
  } catch (e: unknown) {
    toast.error(toast.fromError(e, t('settings.toast.openDataFolderFailed')))
  }
}

async function checkUpdate() {
  updateChecking.value = true
  updateResult.value = null
  downloadStatus.value = 'idle'
  downloadProgress.value = 0
  downloadSpeed.value = ''
  downloadedFilePath.value = ''
  downloadedFileName.value = ''
  try {
    const result = await api.system.checkUpdate()
    updateResult.value = result
    if (result.hasUpdate) {
      toast.success(t('settings.toast.foundNewVersion', { latest: result.latestVersion, current: result.currentVersion }), {
        duration: 5000,
        description: t('settings.toast.canDownloadBelow'),
      })
    } else {
      // 后端在限流/网络失败/无 release/版本相同等情况返回不同 message，
      // 直接显示后端 message 让用户知晓真实状态(而非一律显示"已是最新版本")
      toast.info(result.message || t('update.message.latest', { version: result.currentVersion }))
    }
  } catch (e) {
    const apiError = ApiError.from(e)
    toast.error(t('settings.toast.checkUpdateFailed', { error: apiError.message }), { duration: 5000 })
  } finally {
    updateChecking.value = false
  }
}

function formatSpeed(bytesPerSecond: number): string {
  if (!bytesPerSecond || bytesPerSecond <= 0) return ''
  const units = ['B/s', 'KB/s', 'MB/s', 'GB/s']
  let i = 0
  let speed = bytesPerSecond
  while (speed >= 1024 && i < units.length - 1) {
    speed /= 1024
    i++
  }
  return `${speed.toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

async function startDownload() {
  if (!updateResult.value?.downloadUrl) return
  downloadStatus.value = 'downloading'
  downloadProgress.value = 0
  downloadSpeed.value = ''
  downloadedFilePath.value = ''
  downloadedFileName.value = ''
  try {
    await api.system.startDownloadUpdate(updateResult.value.downloadUrl)
  } catch (e) {
    downloadStatus.value = 'error'
    const apiError = ApiError.from(e)
    toast.error(t('settings.toast.startDownloadFailed', { error: apiError.message }))
  }
}

async function cancelDownload() {
  try {
    await api.system.cancelDownload()
    downloadStatus.value = 'idle'
    downloadProgress.value = 0
    downloadSpeed.value = ''
  } catch (e) {
    // ignore
  }
}

async function openDownloadedFile() {
  if (!downloadedFilePath.value) return
  try {
    await api.system.openDownloadedFile(downloadedFilePath.value)
  } catch (e) {
    const apiError = ApiError.from(e)
    toast.error(t('settings.toast.openFileFailed', { error: apiError.message }))
  }
}

// === Skills 仓库扫描管理 ===
// 调用后端 AddSkillRepo / RemoveSkillRepo，后端负责持久化与去重
// 解析仓库地址，支持以下格式：
//   owner/name
//   https://github.com/owner/name
//   https://github.com/owner/name/tree/branch
//   git@github.com:owner/name.git
// 分支：从 URL 的 /tree/<branch> 提取，未指定时默认 main
function parseRepoUrl(url: string): { owner: string; name: string; branch: string } | null {
  const trimmed = url.trim()
  if (!trimmed) return null

  let s = trimmed
  let branch = ''

  // SSH 格式: git@github.com:owner/name.git
  const sshMatch = s.match(/^git@github\.com:(.+?)\.git$/i)
  if (sshMatch) {
    s = sshMatch[1]
  } else {
    // 去除 https://github.com/ 或 github.com/ 前缀
    s = s.replace(/^https?:\/\/github\.com\//i, '')
    s = s.replace(/^github\.com\//i, '')
    // 去除 .git 后缀
    s = s.replace(/\.git$/i, '')
    // 提取 /tree/<branch> 中的分支
    const treeMatch = s.match(/\/tree\/([^/]+)/i)
    if (treeMatch) {
      branch = treeMatch[1]
      s = s.replace(/\/tree\/[^/]+.*$/i, '')
    }
  }

  const parts = s.split('/').filter(Boolean)
  if (parts.length < 2) return null
  const owner = parts[0]
  const name = parts[1]
  if (!owner || !name) return null
  return { owner, name, branch: branch || 'main' }
}

async function refreshSkillRepos() {
  const fresh = await api.settings.get()
  settings.config.skillRepos = fresh.skillRepos ?? []
  // 标记仓库列表变更，让 Market 页面挂载时检测并刷新
  settings.markSkillReposChanged()
  // 通知市场页面重新搜索 skills（后端已清理缓存）
  events.emit('skills:repos-changed')
}

async function addSkillRepo() {
  const parsed = parseRepoUrl(newRepo.value.url)
  if (!parsed) {
    repoError.value = t('settings.skills.invalidRepoUrl')
    return
  }
  repoError.value = null
  repoBusy.value = true
  try {
    const branch = newRepo.value.branch.trim() || parsed.branch
    await api.market.addSkillRepo({ owner: parsed.owner, name: parsed.name, branch })
    newRepo.value = { url: '', branch: '' }
    await refreshSkillRepos()
  } catch (e) {
    const apiError = ApiError.from(e)
    repoError.value = apiError.message
  } finally {
    repoBusy.value = false
  }
}

async function removeSkillRepo(repo: SkillRepo) {
  repoBusy.value = true
  repoError.value = null
  try {
    await api.market.removeSkillRepo(repo)
    await refreshSkillRepos()
  } catch (e) {
    const apiError = ApiError.from(e)
    repoError.value = apiError.message
  } finally {
    repoBusy.value = false
  }
}

function startEditRepo(repo: SkillRepo) {
  editingRepo.value = repo
  editForm.value = {
    url: `${repo.owner}/${repo.name}`,
    branch: repo.branch || 'main',
  }
  repoError.value = null
}

function cancelEditRepo() {
  editingRepo.value = null
  editForm.value = { url: '', branch: '' }
}

async function saveEditRepo() {
  if (!editingRepo.value) return
  const parsed = parseRepoUrl(editForm.value.url)
  if (!parsed) {
    repoError.value = t('settings.skills.invalidRepoUrl')
    return
  }
  repoError.value = null
  repoBusy.value = true
  try {
    const branch = editForm.value.branch.trim() || parsed.branch
    await api.market.updateSkillRepo(
      editingRepo.value,
      { owner: parsed.owner, name: parsed.name, branch }
    )
    await refreshSkillRepos()
    cancelEditRepo()
  } catch (e) {
    const apiError = ApiError.from(e)
    repoError.value = apiError.message
  } finally {
    repoBusy.value = false
  }
}

// 市场来源的展示元数据：分为 MCP 和 Skills 两类
const marketSourceTabs = computed(() => ({
  mcp: {
    label: 'MCP',
    sources: [
      { key: 'official', label: 'Official', description: t('settings.market.officialDesc') },
      { key: 'smithery', label: 'Smithery', description: t('settings.market.smitheryDesc') },
    ],
  },
  skills: {
    label: 'Skills',
    sources: [
      { key: 'github', label: 'GitHub Skills', description: t('settings.market.githubDesc') },
      { key: 'skills-sh', label: 'skills.sh', description: t('settings.market.skillsShDesc') },
    ],
  },
}))

// 当前选中的市场来源类型 tab
const activeMarketTab = ref<'mcp' | 'skills'>('mcp')

const skillRepos = computed(() => settings.config.skillRepos ?? [])

// 获取当前 tab 下的来源列表
const marketSourceList = computed(() => {
  const sources = settings.config.marketSources ?? {}
  const tabSources = marketSourceTabs.value[activeMarketTab.value].sources
  return tabSources
    .filter(m => sources[m.key])
    .map(m => ({ ...m, enabled: sources[m.key]?.enabled ?? false }))
})
</script>

<template>
  <div class="flex h-full flex-col">
    <!-- 固定头部 -->
    <div class="shrink-0 border-b border-border px-8 pt-8 pb-4">
      <div class="mx-auto max-w-4xl">
        <h1 class="text-2xl font-semibold tracking-tight">{{ t('settings.title') }}</h1>
        <p class="mt-1 text-sm text-muted-foreground">{{ t('settings.subtitle') }}</p>
      </div>
    </div>

    <!-- 可滚动内容 -->
    <div class="flex-1 overflow-y-auto">
      <div class="mx-auto max-w-4xl space-y-6 px-8 py-4">

    <Card>
      <CardHeader>
        <CardTitle>{{ t('settings.appearance') }}</CardTitle>
        <CardDescription>{{ t('settings.appearanceDesc') }}</CardDescription>
      </CardHeader>
      <CardContent class="space-y-3">
        <div class="flex items-center justify-between">
          <Label>{{ t('settings.theme') }}</Label>
          <Tabs :model-value="settings.config.theme" @update:model-value="(v: any) => setTheme(v)" class="w-fit">
            <TabsList>
              <TabsTrigger value="light">{{ t('settings.themeOptions.light') }}</TabsTrigger>
              <TabsTrigger value="dark">{{ t('settings.themeOptions.dark') }}</TabsTrigger>
              <TabsTrigger value="system">{{ t('settings.themeOptions.system') }}</TabsTrigger>
            </TabsList>
          </Tabs>
        </div>
        <div class="flex items-center justify-between">
          <Label>{{ t('settings.language') }}</Label>
          <Tabs :model-value="settings.config.language" @update:model-value="(v: any) => setLanguage(v)" class="w-fit">
            <TabsList>
              <TabsTrigger value="">{{ t('settings.languageOptions.system') }}</TabsTrigger>
              <TabsTrigger value="zh-CN">{{ t('settings.languageOptions.zhCN') }}</TabsTrigger>
              <TabsTrigger value="en">{{ t('settings.languageOptions.en') }}</TabsTrigger>
            </TabsList>
          </Tabs>
        </div>
      </CardContent>
    </Card>

    <Card>
      <CardHeader>
        <CardTitle>{{ t('settings.window.title') }}</CardTitle>
        <CardDescription>{{ t('settings.window.desc') }}</CardDescription>
      </CardHeader>
      <CardContent class="space-y-3">
        <div class="flex items-start justify-between">
          <Label class="mt-2">{{ t('settings.window.behaviorLabel') }}</Label>
          <div class="flex flex-col items-center gap-1.5">
            <Tabs :model-value="settings.config.windowAction || 'minimize'" @update:model-value="(v: any) => setWindowAction(v)" class="w-fit">
              <TabsList>
                <TabsTrigger value="minimize">{{ t('settings.window.action.minimize') }}</TabsTrigger>
                <TabsTrigger value="exit">{{ t('settings.window.action.exit') }}</TabsTrigger>
              </TabsList>
            </Tabs>
            <label class="flex items-center gap-2 cursor-pointer select-none">
              <Checkbox
                :model-value="settings.config.windowNoRemind ?? false"
                @update:model-value="(v) => setWindowNoRemind(v === true)"
              />
              <span class="text-sm text-muted-foreground">{{ t('settings.window.noRemind') }}</span>
            </label>
          </div>
        </div>
      </CardContent>
    </Card>

    <Card>
      <CardHeader>
        <CardTitle>Skills</CardTitle>
        <CardDescription>{{ t('settings.skills.desc') }}</CardDescription>
      </CardHeader>
      <CardContent class="space-y-4">
        <div class="flex items-center justify-between">
          <div>
            <Label>{{ t('settings.skills.storage') }}</Label>
            <p class="text-xs text-muted-foreground">{{ t('settings.skills.storageHint') }}</p>
          </div>
          <Tabs :model-value="settings.config.skillStorage" @update:model-value="(v: any) => setSkillStorage(v)" class="w-fit">
            <TabsList>
              <TabsTrigger value="agentpack">~/.agentpack/skills/</TabsTrigger>
              <TabsTrigger value="unified">~/.agents/skills/</TabsTrigger>
            </TabsList>
          </Tabs>
        </div>
        <Separator />
        <div class="flex items-center justify-between">
          <div>
            <Label>{{ t('settings.skills.syncMethod') }}</Label>
            <p class="text-xs text-muted-foreground">{{ t('settings.skills.syncHint') }}</p>
          </div>
          <Tabs :model-value="settings.config.skillSyncMethod" @update:model-value="(v: any) => setSkillSyncMethod(v)" class="w-fit">
            <TabsList>
              <TabsTrigger value="symlink">Symlink</TabsTrigger>
              <TabsTrigger value="copy">Copy</TabsTrigger>
            </TabsList>
          </Tabs>
        </div>
      </CardContent>
    </Card>

    <Card>
      <CardHeader>
        <CardTitle>{{ t('settings.skills.reposTitle') }}</CardTitle>
        <CardDescription>{{ t('settings.skills.reposDesc') }}</CardDescription>
      </CardHeader>
      <CardContent class="space-y-4">
        <div v-if="skillRepos.length === 0" class="text-xs text-muted-foreground">
          {{ t('settings.skills.reposEmpty') }}
        </div>
        <div
          v-for="repo in skillRepos"
          :key="`${repo.owner}/${repo.name}`"
          class="flex items-center justify-between rounded-md border border-border px-3 py-2"
        >
          <template v-if="!editingRepo || editingRepo.owner !== repo.owner || editingRepo.name !== repo.name">
            <div class="min-w-0">
              <div class="text-sm font-medium">
                {{ repo.owner }}/{{ repo.name }}
              </div>
              <div class="text-[11px] text-muted-foreground">
                {{ t('settings.skills.branch') }}：{{ repo.branch || 'main' }}
              </div>
            </div>
            <div class="flex items-center gap-2">
              <Button
                variant="outline"
                size="icon"
                class="h-7 w-7"
                :disabled="repoBusy"
                :aria-label="t('settings.skills.editRepoAria', { name: `${repo.owner}/${repo.name}` })"
                @click="startEditRepo(repo)"
              >
                <PhPencilSimple :size="14" />
              </Button>
              <Button
                variant="outline"
                size="icon"
                class="h-7 w-7 border-destructive/40 text-destructive hover:bg-destructive/10"
                :disabled="repoBusy"
                :aria-label="t('settings.skills.removeRepoAria', { name: `${repo.owner}/${repo.name}` })"
                @click="removeSkillRepo(repo)"
              >
                <PhTrash :size="14" class="text-destructive" />
              </Button>
            </div>
          </template>
          <template v-else>
            <div class="flex flex-1 items-center gap-2">
              <Input
                v-model="editForm.url"
                placeholder="anthropics/skills"
                class="flex-1"
                :aria-label="t('settings.skills.repoUrl')"
                @keyup.enter="saveEditRepo"
              />
              <div class="w-28">
                <Input
                  v-model="editForm.branch"
                  placeholder="main"
                  :aria-label="t('settings.skills.branchName')"
                  @keyup.enter="saveEditRepo"
                />
              </div>
              <Button
                size="sm"
                variant="default"
                :disabled="repoBusy"
                @click="saveEditRepo"
              >
                {{ t('common.save') }}
              </Button>
              <Button
                size="sm"
                variant="ghost"
                :disabled="repoBusy"
                @click="cancelEditRepo"
              >
                {{ t('common.cancel') }}
              </Button>
            </div>
          </template>
        </div>

        <Separator />

        <div class="space-y-2">
          <Label>{{ t('settings.skills.addRepo') }}</Label>
          <div class="flex gap-2">
            <Input
              v-model="newRepo.url"
              :placeholder="t('settings.skills.repoUrlPlaceholder')"
              class="flex-1"
              :aria-label="t('settings.skills.repoUrl')"
              @keyup.enter="addSkillRepo"
            />
            <div class="w-28">
              <Input
                v-model="newRepo.branch"
                placeholder="main"
                :aria-label="t('settings.skills.branchName')"
                @keyup.enter="addSkillRepo"
              />
            </div>
            <Button
              size="sm"
              variant="default"
              :disabled="repoBusy || !newRepo.url.trim()"
              @click="addSkillRepo"
            >
              <PhPlus :size="14" />
              {{ t('common.add') }}
            </Button>
          </div>
          <p class="text-[11px] text-muted-foreground">{{ t('settings.skills.repoHint') }}</p>
          <p v-if="repoError" class="text-xs text-destructive">{{ repoError }}</p>
        </div>
      </CardContent>
    </Card>

    <Card>
      <CardHeader>
        <CardTitle>{{ t('settings.agents.title') }}</CardTitle>
        <CardDescription>{{ t('settings.agents.desc') }}</CardDescription>
      </CardHeader>
      <CardContent>
        <div v-if="agents.items.length > 0" class="flex flex-wrap items-center gap-2">
          <div
            v-for="agent in agents.sorted"
            :key="agent.id"
            class="flex items-center"
          >
            <AgentToggleButton
              :agent-id="agent.id"
              :agent-name="agentDisplayName(agent)"
              :model-value="agent.status === 'enabled'"
              :disabled="agent.status === 'error' || agent.status === 'not_found'"
              :badge="variantToBadge(agent.variant || getVariantFromId(agent.id))"
              @update:model-value="(v) => toggleAgent(agent.id, v)"
            />
          </div>
        </div>
        <div v-else class="py-2 text-center text-xs text-muted-foreground">
          {{ t('settings.agents.empty') }}
        </div>
      </CardContent>
    </Card>

    <Card>
      <CardHeader>
        <CardTitle>{{ t('settings.market.title') }}</CardTitle>
        <CardDescription>{{ t('settings.market.desc') }}</CardDescription>
      </CardHeader>
      <CardContent class="space-y-4">
        <!-- Tab 切换 -->
        <div class="flex items-center justify-between">
          <Label>{{ t('settings.market.sourceType') }}</Label>
          <Tabs v-model="activeMarketTab" class="w-fit">
            <TabsList>
              <TabsTrigger v-for="tab in (['mcp', 'skills'] as const)" :key="tab" :value="tab">
                {{ marketSourceTabs[tab].label }}
              </TabsTrigger>
            </TabsList>
          </Tabs>
        </div>

        <!-- 来源列表 -->
        <div class="space-y-3">
          <div
            v-for="src in marketSourceList"
            :key="src.key"
            class="flex items-center justify-between rounded-md border border-border px-3 py-2.5"
          >
            <div>
              <div class="text-sm font-medium">{{ src.label }}</div>
              <div class="text-xs text-muted-foreground">{{ src.description }}</div>
            </div>
            <Switch
              :model-value="src.enabled"
              @update:model-value="(v) => setMarketSource(src.key, v)"
            />
          </div>
        </div>
      </CardContent>
    </Card>

    <Card>
      <CardHeader>
        <CardTitle>{{ t('settings.backup.title') }}</CardTitle>
        <CardDescription>{{ t('settings.backup.desc') }}</CardDescription>
      </CardHeader>
      <CardContent class="space-y-3">
        <div class="flex items-center justify-between">
          <div>
            <Label>{{ t('settings.backup.autoBackup') }}</Label>
            <p class="text-xs text-muted-foreground">{{ t('settings.backup.autoBackupDesc') }}</p>
          </div>
          <Switch
            :model-value="settings.config.autoBackup"
            @update:model-value="setAutoBackup"
          />
        </div>
        <Separator />
        <div class="flex items-center justify-between">
          <Label>{{ t('settings.backup.retention') }}</Label>
          <Input
            :model-value="String(settings.config.backupRetention || settings.config.backupCount)"
            type="number"
            class="w-20"
            @update:model-value="setBackupRetention"
          />
        </div>
        <div class="flex gap-2">
          <Button variant="outline" size="sm" :disabled="backupLoading !== null" @click="createBackup">
            <PhArrowsClockwise :size="14" :class="{ 'animate-spin': backupLoading === 'create' }" />
            <span>{{ backupLoading === 'create' ? t('settings.backup.creating') : t('settings.backup.createNow') }}</span>
          </Button>
          <Button variant="outline" size="sm" @click="openConfigFolder">
            <PhFolderOpen :size="14" />
            <span>{{ t('settings.backup.openDataFolder') }}</span>
          </Button>
        </div>
      </CardContent>
    </Card>

    <Card>
      <CardHeader>
        <CardTitle>{{ t('settings.backup.importExportTitle') }}</CardTitle>
        <CardDescription>{{ t('settings.backup.importExportDesc') }}</CardDescription>
      </CardHeader>
      <CardContent class="space-y-3">
        <div class="flex gap-2">
          <Button variant="outline" size="sm" :disabled="backupLoading !== null" @click="exportData">
            <PhUpload :size="14" :class="{ 'animate-pulse': backupLoading === 'export' }" />
            <span>{{ backupLoading === 'export' ? t('settings.backup.exporting') : t('settings.backup.exportToFile') }}</span>
          </Button>
          <Button variant="outline" size="sm" :disabled="backupLoading !== null" @click="importData">
            <PhDownload :size="14" :class="{ 'animate-pulse': backupLoading === 'import' }" />
            <span>{{ backupLoading === 'import' ? t('settings.backup.importing') : t('settings.backup.importFromFile') }}</span>
          </Button>
        </div>
      </CardContent>
    </Card>

    <Card>
      <CardHeader>
        <CardTitle>{{ t('settings.about.title') }}</CardTitle>
        <CardDescription>{{ t('settings.about.desc') }}</CardDescription>
      </CardHeader>
      <CardContent class="space-y-3">
        <div class="flex items-center justify-between">
          <Label>{{ t('settings.update.currentVersion') }}</Label>
          <span class="font-mono text-sm text-muted-foreground">v{{ appVersion }}</span>
        </div>
        <Separator />
        <div class="flex items-center justify-between">
          <Label>{{ t('settings.update.checkUpdate') }}</Label>
          <div class="flex gap-2">
            <Button v-if="updateResult?.changelog" variant="outline" size="sm" @click="changelogDialogOpen = true">
              <span>{{ t('settings.update.changelog') }}</span>
            </Button>
            <Button variant="outline" size="sm" :disabled="updateChecking" @click="checkUpdate">
              <PhArrowsClockwise :size="14" :class="{ 'animate-spin': updateChecking }" />
              <span>{{ updateChecking ? t('settings.update.checking') : t('settings.update.checkUpdate') }}</span>
            </Button>
          </div>
        </div>

        <!-- 下载进度 -->
        <template v-if="updateResult?.hasUpdate">
          <Separator />
          <div class="space-y-2">
            <div class="flex items-center justify-between">
              <Label>{{ t('settings.update.downloadSection') }}</Label>
              <span v-if="updateResult.downloadName" class="text-xs text-muted-foreground">{{ updateResult.downloadName }}</span>
            </div>

            <!-- 空闲状态：显示下载按钮 -->
            <div v-if="downloadStatus === 'idle'" class="flex gap-2">
              <Button size="sm" variant="default" @click="startDownload">
                <PhDownload :size="14" />
                <span>{{ t('settings.update.download') }}</span>
              </Button>
            </div>

            <!-- 下载中：显示进度条 -->
            <div v-if="downloadStatus === 'downloading'" class="space-y-1.5">
              <div class="flex items-center gap-2">
                <div class="h-2 flex-1 overflow-hidden rounded-full bg-muted">
                  <div
                    class="h-full rounded-full bg-primary transition-all duration-200"
                    :style="{ width: downloadProgress + '%' }"
                  />
                </div>
                <span class="w-14 text-right text-xs tabular-nums text-muted-foreground">{{ downloadProgress }}%</span>
              </div>
              <div class="flex items-center justify-between">
                <span v-if="downloadSpeed" class="text-xs text-muted-foreground">{{ downloadSpeed }}</span>
                <Button variant="ghost" size="sm" class="h-auto px-1 text-xs text-destructive" @click="cancelDownload">
                  {{ t('settings.update.cancelDownload') }}
                </Button>
              </div>
            </div>

            <!-- 下载完成 -->
            <div v-if="downloadStatus === 'complete'" class="flex gap-2">
              <Button size="sm" variant="default" @click="openDownloadedFile">
                <PhFolderOpen :size="14" />
                <span>{{ t('settings.update.openFile') }}</span>
              </Button>
              <Button size="sm" variant="outline" @click="checkUpdate">
                <PhArrowsClockwise :size="14" />
                <span>{{ t('settings.update.recheck') }}</span>
              </Button>
            </div>

            <!-- 下载失败 -->
            <div v-if="downloadStatus === 'error'" class="flex gap-2">
              <Button size="sm" variant="outline" @click="startDownload">
                <PhArrowsClockwise :size="14" />
                <span>{{ t('settings.update.retryDownload') }}</span>
              </Button>
            </div>
          </div>
        </template>
      </CardContent>
    </Card>

    <!-- 导入确认弹窗 -->
    <Dialog v-model:open="importDialog.open">
      <DialogContent class="max-w-md">
        <DialogHeader>
          <DialogTitle>{{ t('settings.importDialog.title') }}</DialogTitle>
          <DialogDescription>{{ t('settings.importDialog.desc') }}</DialogDescription>
        </DialogHeader>
        <div class="space-y-4 py-2">
          <div class="rounded-md border border-border bg-muted/30 px-3 py-2 text-xs text-muted-foreground break-all">
            {{ importDialog.filePath }}
          </div>
          <div class="space-y-3">
            <label class="flex items-center gap-2 cursor-pointer">
              <Checkbox
                :model-value="importDialog.applyAgentStatus"
                @update:model-value="(v) => importDialog.applyAgentStatus = v === true"
              />
              <span class="text-sm">{{ t('settings.importDialog.restoreAgentStatus') }}</span>
            </label>
            <label class="flex items-center gap-2 cursor-pointer">
              <Checkbox
                :model-value="importDialog.applySettings"
                @update:model-value="(v) => importDialog.applySettings = v === true"
              />
              <span class="text-sm">{{ t('settings.importDialog.restoreAppSettings') }}</span>
            </label>
          </div>
          <div class="space-y-2">
            <Label class="text-sm">{{ t('settings.importDialog.mcpStrategy') }}</Label>
            <RadioGroup
              :model-value="importDialog.overwrite ? 'overwrite' : 'skip'"
              @update:model-value="(v) => importDialog.overwrite = String(v) === 'overwrite'"
            >
              <div class="flex items-center gap-2">
                <RadioGroupItem value="skip" />
                <span class="text-sm">{{ t('settings.importDialog.skipExisting') }}</span>
              </div>
              <div class="flex items-center gap-2">
                <RadioGroupItem value="overwrite" />
                <span class="text-sm">{{ t('settings.importDialog.overwriteExisting') }}</span>
              </div>
            </RadioGroup>
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" size="sm" @click="importDialog.open = false">{{ t('common.cancel') }}</Button>
          <Button size="sm" @click="confirmImport">{{ t('settings.importDialog.importBtn') }}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <!-- 更新日志弹窗 -->
    <Dialog v-model:open="changelogDialogOpen">
      <DialogContent class="max-w-2xl max-h-[80vh] flex flex-col">
        <DialogHeader>
          <DialogTitle>{{ t('settings.update.changelog') }}</DialogTitle>
          <DialogDescription v-if="updateResult">
            v{{ updateResult.currentVersion }}
            <span v-if="updateResult.hasUpdate"> → v{{ updateResult.latestVersion }}</span>
          </DialogDescription>
        </DialogHeader>
        <div class="flex-1 overflow-y-auto">
          <div class="text-sm text-muted-foreground leading-relaxed max-w-none [&_a]:text-primary [&_a]:underline [&_h1]:text-base [&_h1]:font-semibold [&_h1]:mt-4 [&_h1]:mb-2 [&_h2]:text-sm [&_h2]:font-semibold [&_h2]:mt-3 [&_h2]:mb-2 [&_ul]:list-disc [&_ul]:pl-5 [&_ol]:list-decimal [&_ol]:pl-5 [&_li]:my-1 [&_pre]:bg-muted [&_pre]:p-3 [&_pre]:rounded [&_pre]:overflow-x-auto [&_code]:text-primary [&_hr]:my-4 [&_blockquote]:border-l-2 [&_blockquote]:border-border [&_blockquote]:pl-3 [&_blockquote]:italic" v-html="renderMarkdown(updateResult?.changelog || '')" />
        </div>
        <DialogFooter>
          <Button v-if="updateResult?.releaseUrl" variant="outline" size="sm" @click="api.system.openUrl(updateResult.releaseUrl)">
            <PhDownload :size="14" />
            <span>{{ t('settings.update.goToReleases') }}</span>
          </Button>
          <Button v-if="updateResult?.hasUpdate && downloadStatus === 'idle'" size="sm" @click="startDownload">
            <PhDownload :size="14" />
            <span>{{ t('settings.update.download') }}</span>
          </Button>
          <Button variant="outline" size="sm" @click="changelogDialogOpen = false">
            <span>{{ t('common.close') }}</span>
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

      </div>
    </div>
  </div>
</template>
