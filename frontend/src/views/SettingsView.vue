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
      toast.error(`下载失败：${data.message || '未知错误'}`)
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
    toast.error(toast.fromError(e, '保存失败'))
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
    toast.success(`已${enabled ? '启用' : '禁用'} Agent`)
  } catch (e) {
    const apiError = ApiError.from(e)
    toast.error(`切换 Agent 状态失败：${apiError.message}`)
  }
}

async function createBackup() {
  backupLoading.value = 'create'
  try {
    await api.backup.create('手动备份')
    toast.success('已创建备份')
  } catch (e) {
    const apiError = ApiError.from(e)
    toast.error(`备份失败：${apiError.message}`)
  } finally {
    backupLoading.value = null
  }
}

async function exportData() {
  backupLoading.value = 'export'
  try {
    const exportDir = await api.system.pickDirectory()
    if (!exportDir) return
    const summary = await api.backup.create('导出备份')
    const backupId = summary.id || ''
    if (!backupId) {
      toast.error('创建备份失败：未返回备份 ID')
      return
    }
    const dest = `${exportDir}/agentpack-export-${Date.now()}.json`
    await api.export.exportData(backupId, dest)
    toast.success(`已导出到 ${dest}`)
  } catch (e) {
    const apiError = ApiError.from(e)
    toast.error(`导出失败：${apiError.message}`)
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
    toast.error(`选择文件失败：${apiError.message}`)
  }
}

async function confirmImport() {
  const { filePath, overwrite, applyAgentStatus, applySettings } = importDialog.value
  importDialog.value.open = false
  backupLoading.value = 'import'
  try {
    const result = await api.export.importData(filePath, { overwrite, applyAgentStatus, applySettings })
    const parts: string[] = []
    if (result.mcpApplied > 0) parts.push(`${result.mcpApplied} 个服务器已导入`)
    if (result.mcpSkipped > 0) parts.push(`${result.mcpSkipped} 个已跳过`)
    if (result.agentStatusApplied > 0) parts.push(`${result.agentStatusApplied} 个 Agent 状态已恢复`)
    if (applySettings && result.exportedSettings) parts.push('应用设置已恢复')
    toast.success(parts.length > 0 ? `导入完成：${parts.join('，')}` : '导入完成')
  } catch (e) {
    const apiError = ApiError.from(e)
    toast.error(`导入失败：${apiError.message}`)
  } finally {
    backupLoading.value = null
  }
}

async function openConfigFolder() {
  try {
    await api.system.openConfigFolder()
  } catch (e: unknown) {
    toast.error(toast.fromError(e, '无法打开数据目录'))
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
      toast.success(`发现新版本 v${result.latestVersion}（当前 v${result.currentVersion}）`, {
        duration: 5000,
        description: '可在下方下载安装包',
      })
    } else {
      toast.success(`当前已是最新版本 v${result.currentVersion}`)
    }
  } catch (e) {
    const apiError = ApiError.from(e)
    toast.error(`检查更新失败：${apiError.message}`, { duration: 5000 })
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
    toast.error(`启动下载失败：${apiError.message}`)
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
    toast.error(`打开文件失败：${apiError.message}`)
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
    repoError.value = '请输入有效的仓库地址，如 anthropics/skills'
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
    repoError.value = '请输入有效的仓库地址，如 anthropics/skills'
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
const marketSourceTabs = {
  mcp: {
    label: 'MCP',
    sources: [
      { key: 'official', label: 'Official', description: '官方 MCP 注册表' },
      { key: 'smithery', label: 'Smithery', description: 'Smithery Registry MCP 服务器' },
    ] as const,
  },
  skills: {
    label: 'Skills',
    sources: [
      { key: 'github', label: 'GitHub Skills', description: 'GitHub 仓库扫描 Skills' },
      { key: 'skills-sh', label: 'skills.sh', description: 'skills.sh Skills 市场' },
    ] as const,
  },
}

// 当前选中的市场来源类型 tab
const activeMarketTab = ref<'mcp' | 'skills'>('mcp')

const skillRepos = computed(() => settings.config.skillRepos ?? [])

// 获取当前 tab 下的来源列表
const marketSourceList = computed(() => {
  const sources = settings.config.marketSources ?? {}
  const tabSources = marketSourceTabs[activeMarketTab.value].sources
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
        <h1 class="text-2xl font-semibold tracking-tight">设置</h1>
        <p class="mt-1 text-sm text-muted-foreground">应用偏好设置与数据管理。</p>
      </div>
    </div>

    <!-- 可滚动内容 -->
    <div class="flex-1 overflow-y-auto">
      <div class="mx-auto max-w-4xl space-y-6 px-8 py-4">

    <Card>
      <CardHeader>
        <CardTitle>外观</CardTitle>
        <CardDescription>主题与视觉偏好。</CardDescription>
      </CardHeader>
      <CardContent class="space-y-3">
        <div class="flex items-center justify-between">
          <Label>主题</Label>
          <Tabs :model-value="settings.config.theme" @update:model-value="(v: any) => setTheme(v)" class="w-fit">
            <TabsList>
              <TabsTrigger value="light">浅色</TabsTrigger>
              <TabsTrigger value="dark">深色</TabsTrigger>
              <TabsTrigger value="system">跟随系统</TabsTrigger>
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
        <CardTitle>窗口行为</CardTitle>
        <CardDescription>点击关闭按钮时的行为。</CardDescription>
      </CardHeader>
      <CardContent class="space-y-3">
        <div class="flex items-start justify-between">
          <Label class="mt-2">关闭按钮行为</Label>
          <div class="flex flex-col items-center gap-1.5">
            <Tabs :model-value="settings.config.windowAction || 'minimize'" @update:model-value="(v: any) => setWindowAction(v)" class="w-fit">
              <TabsList>
                <TabsTrigger value="minimize">最小化到托盘</TabsTrigger>
                <TabsTrigger value="exit">退出</TabsTrigger>
              </TabsList>
            </Tabs>
            <label class="flex items-center gap-2 cursor-pointer select-none">
              <Checkbox
                :model-value="settings.config.windowNoRemind ?? false"
                @update:model-value="(v) => setWindowNoRemind(v === true)"
              />
              <span class="text-sm text-muted-foreground">不再提醒</span>
            </label>
          </div>
        </div>
      </CardContent>
    </Card>

    <Card>
      <CardHeader>
        <CardTitle>Skills</CardTitle>
        <CardDescription>Skills 存储位置与同步方式配置。</CardDescription>
      </CardHeader>
      <CardContent class="space-y-4">
        <div class="flex items-center justify-between">
          <div>
            <Label>存储位置</Label>
            <p class="text-xs text-muted-foreground">选择 Skills 的 SSOT 目录</p>
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
            <Label>同步方式</Label>
            <p class="text-xs text-muted-foreground">Skills 同步到 Agent 目录的方式</p>
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
        <CardTitle>Skills 仓库扫描</CardTitle>
        <CardDescription>配置市场 Skills Tab 扫描的 GitHub 仓库列表。</CardDescription>
      </CardHeader>
      <CardContent class="space-y-4">
        <div v-if="skillRepos.length === 0" class="text-xs text-muted-foreground">
          暂无扫描仓库。添加仓库后，市场 Skills Tab 会扫描其中含 SKILL.md 的目录。
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
                分支：{{ repo.branch || 'main' }}
              </div>
            </div>
            <div class="flex items-center gap-2">
              <Button
                variant="outline"
                size="icon"
                class="h-7 w-7"
                :disabled="repoBusy"
                :aria-label="`编辑 ${repo.owner}/${repo.name}`"
                @click="startEditRepo(repo)"
              >
                <PhPencilSimple :size="14" />
              </Button>
              <Button
                variant="outline"
                size="icon"
                class="h-7 w-7 border-destructive/40 text-destructive hover:bg-destructive/10"
                :disabled="repoBusy"
                :aria-label="`移除 ${repo.owner}/${repo.name}`"
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
                aria-label="仓库地址"
                @keyup.enter="saveEditRepo"
              />
              <div class="w-28">
                <Input
                  v-model="editForm.branch"
                  placeholder="main"
                  aria-label="分支名称"
                  @keyup.enter="saveEditRepo"
                />
              </div>
              <Button
                size="sm"
                variant="default"
                :disabled="repoBusy"
                @click="saveEditRepo"
              >
                保存
              </Button>
              <Button
                size="sm"
                variant="ghost"
                :disabled="repoBusy"
                @click="cancelEditRepo"
              >
                取消
              </Button>
            </div>
          </template>
        </div>

        <Separator />

        <div class="space-y-2">
          <Label>添加仓库</Label>
          <div class="flex gap-2">
            <Input
              v-model="newRepo.url"
              placeholder="anthropics/skills 或 https://github.com/anthropics/skills"
              class="flex-1"
              aria-label="仓库地址"
              @keyup.enter="addSkillRepo"
            />
            <div class="w-28">
              <Input
                v-model="newRepo.branch"
                placeholder="main"
                aria-label="分支名称"
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
              添加
            </Button>
          </div>
          <p class="text-[11px] text-muted-foreground">支持 owner/name 或完整 GitHub URL，分支默认 main（URL 含 /tree/branch 时自动识别）</p>
          <p v-if="repoError" class="text-xs text-destructive">{{ repoError }}</p>
        </div>
      </CardContent>
    </Card>

    <Card>
      <CardHeader>
        <CardTitle>Agents 管理</CardTitle>
        <CardDescription>启用或禁用已检测 Agents 的配置管理。</CardDescription>
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
          尚未检测到 Agents。
        </div>
      </CardContent>
    </Card>

    <Card>
      <CardHeader>
        <CardTitle>市场来源</CardTitle>
        <CardDescription>启用或禁用各个市场集成。</CardDescription>
      </CardHeader>
      <CardContent class="space-y-4">
        <!-- Tab 切换 -->
        <div class="flex items-center justify-between">
          <Label>来源类型</Label>
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
        <CardTitle>备份</CardTitle>
        <CardDescription>自动与手动备份配置。</CardDescription>
      </CardHeader>
      <CardContent class="space-y-3">
        <div class="flex items-center justify-between">
          <div>
            <Label>自动备份</Label>
            <p class="text-xs text-muted-foreground">每次写入操作前创建备份。</p>
          </div>
          <Switch
            :model-value="settings.config.autoBackup"
            @update:model-value="setAutoBackup"
          />
        </div>
        <Separator />
        <div class="flex items-center justify-between">
          <Label>保留备份数</Label>
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
            <span>{{ backupLoading === 'create' ? '创建中...' : '立即创建备份' }}</span>
          </Button>
          <Button variant="outline" size="sm" @click="openConfigFolder">
            <PhFolderOpen :size="14" />
            <span>打开数据文件夹</span>
          </Button>
        </div>
      </CardContent>
    </Card>

    <Card>
      <CardHeader>
        <CardTitle>导入 / 导出</CardTitle>
        <CardDescription>在 AgentPack 安装之间传输数据。</CardDescription>
      </CardHeader>
      <CardContent class="space-y-3">
        <div class="flex gap-2">
          <Button variant="outline" size="sm" :disabled="backupLoading !== null" @click="exportData">
            <PhUpload :size="14" :class="{ 'animate-pulse': backupLoading === 'export' }" />
            <span>{{ backupLoading === 'export' ? '导出中...' : '导出到文件' }}</span>
          </Button>
          <Button variant="outline" size="sm" :disabled="backupLoading !== null" @click="importData">
            <PhDownload :size="14" :class="{ 'animate-pulse': backupLoading === 'import' }" />
            <span>{{ backupLoading === 'import' ? '导入中...' : '从文件导入' }}</span>
          </Button>
        </div>
      </CardContent>
    </Card>

    <Card>
      <CardHeader>
        <CardTitle>关于</CardTitle>
        <CardDescription>应用版本与更新检查。</CardDescription>
      </CardHeader>
      <CardContent class="space-y-3">
        <div class="flex items-center justify-between">
          <Label>当前版本</Label>
          <span class="font-mono text-sm text-muted-foreground">v{{ appVersion }}</span>
        </div>
        <Separator />
        <div class="flex items-center justify-between">
          <Label>检查更新</Label>
          <div class="flex gap-2">
            <Button v-if="updateResult?.changelog" variant="outline" size="sm" @click="changelogDialogOpen = true">
              <span>更新日志</span>
            </Button>
            <Button variant="outline" size="sm" :disabled="updateChecking" @click="checkUpdate">
              <PhArrowsClockwise :size="14" :class="{ 'animate-spin': updateChecking }" />
              <span>{{ updateChecking ? '检查中...' : '检查更新' }}</span>
            </Button>
          </div>
        </div>

        <!-- 下载进度 -->
        <template v-if="updateResult?.hasUpdate">
          <Separator />
          <div class="space-y-2">
            <div class="flex items-center justify-between">
              <Label>下载更新</Label>
              <span v-if="updateResult.downloadName" class="text-xs text-muted-foreground">{{ updateResult.downloadName }}</span>
            </div>

            <!-- 空闲状态：显示下载按钮 -->
            <div v-if="downloadStatus === 'idle'" class="flex gap-2">
              <Button size="sm" variant="default" @click="startDownload">
                <PhDownload :size="14" />
                <span>下载安装包</span>
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
                  取消下载
                </Button>
              </div>
            </div>

            <!-- 下载完成 -->
            <div v-if="downloadStatus === 'complete'" class="flex gap-2">
              <Button size="sm" variant="default" @click="openDownloadedFile">
                <PhFolderOpen :size="14" />
                <span>打开文件位置</span>
              </Button>
              <Button size="sm" variant="outline" @click="checkUpdate">
                <PhArrowsClockwise :size="14" />
                <span>重新检查</span>
              </Button>
            </div>

            <!-- 下载失败 -->
            <div v-if="downloadStatus === 'error'" class="flex gap-2">
              <Button size="sm" variant="outline" @click="startDownload">
                <PhArrowsClockwise :size="14" />
                <span>重试下载</span>
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
          <DialogTitle>确认导入</DialogTitle>
          <DialogDescription>选择导入选项后点击"导入"按钮执行。</DialogDescription>
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
              <span class="text-sm">恢复 Agent 启用/禁用状态</span>
            </label>
            <label class="flex items-center gap-2 cursor-pointer">
              <Checkbox
                :model-value="importDialog.applySettings"
                @update:model-value="(v) => importDialog.applySettings = v === true"
              />
              <span class="text-sm">恢复应用设置（主题、备份配置等）</span>
            </label>
          </div>
          <div class="space-y-2">
            <Label class="text-sm">MCP 服务器导入策略</Label>
            <RadioGroup
              :model-value="importDialog.overwrite ? 'overwrite' : 'skip'"
              @update:model-value="(v) => importDialog.overwrite = String(v) === 'overwrite'"
            >
              <div class="flex items-center gap-2">
                <RadioGroupItem value="skip" />
                <span class="text-sm">跳过已存在的服务器</span>
              </div>
              <div class="flex items-center gap-2">
                <RadioGroupItem value="overwrite" />
                <span class="text-sm">覆盖已存在的服务器</span>
              </div>
            </RadioGroup>
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" size="sm" @click="importDialog.open = false">取消</Button>
          <Button size="sm" @click="confirmImport">导入</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <!-- 更新日志弹窗 -->
    <Dialog v-model:open="changelogDialogOpen">
      <DialogContent class="max-w-2xl max-h-[80vh] flex flex-col">
        <DialogHeader>
          <DialogTitle>更新日志</DialogTitle>
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
            <span>前往 Releases</span>
          </Button>
          <Button v-if="updateResult?.hasUpdate && downloadStatus === 'idle'" size="sm" @click="startDownload">
            <PhDownload :size="14" />
            <span>下载安装包</span>
          </Button>
          <Button variant="outline" size="sm" @click="changelogDialogOpen = false">
            <span>关闭</span>
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

      </div>
    </div>
  </div>
</template>
