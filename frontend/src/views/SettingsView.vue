<script lang="ts">
</script>

<script setup lang="ts">
import { ref, computed, onMounted, onActivated, onDeactivated } from 'vue'
import { useSettingsStore } from '@/stores/settings'
import { Card, CardContent, CardHeader, CardTitle, CardDescription, Switch, Button, Separator, Input, Label, Tabs, TabsList, TabsTrigger, Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter, Checkbox, RadioGroup, RadioGroupItem } from '@/components/ui'

import { PhFolderOpen, PhArrowsClockwise, PhDownload, PhUpload, PhPlus, PhTrash, PhPencilSimple } from '@phosphor-icons/vue'
import { api, ApiError, events, type SkillRepo, type UpdateCheckResult } from '@/lib/api'

import { useToast } from '@/composables/useToast'
import { useI18n } from 'vue-i18n'

const settings = useSettingsStore()
const toast = useToast()
const { t } = useI18n()

const scrollContainer = ref<HTMLElement | null>(null)

const saving = ref(false)
const backupLoading = ref<'create' | 'export' | 'import' | null>(null)
const updateChecking = ref(false)
const updateResult = ref<UpdateCheckResult | null>(null)
let saveDirty = false

// 版本号（从后端 API 获取）
const appVersion = ref('')

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
  // 主动拉取一次设置，防止 store 在其他页面操作后陈旧
  void settings.fetch()
  // 从后端获取版本号
  api.system.getAppVersion().then(v => { appVersion.value = v }).catch(() => {})
})
// KeepAlive 下用 onActivated/onDeactivated 管理事件订阅，避免缓存视图与 App.vue 双重处理
onActivated(() => {
  offSettingsChanged = events.on('settings:changed', () => {
    void settings.fetch()
  })
})
onDeactivated(() => {
  if (offSettingsChanged) offSettingsChanged()
})

const MIN_RETENTION = 1
const MAX_RETENTION = 100
const MIN_LITE_DELAY = 1
const MAX_LITE_DELAY = 120

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
    await autoSave(cloneConfig())
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

function setLiteAutoEnabled(v: boolean) {
  withAutoSave(cfg => { cfg.liteAutoEnabled = v })
}

function setLiteAutoDelay(v: string | number) {
  const parsed = Number(v)
  const value = Number.isFinite(parsed)
    ? Math.min(Math.max(Math.trunc(parsed), MIN_LITE_DELAY), MAX_LITE_DELAY)
    : MIN_LITE_DELAY
  withAutoSave(cfg => { cfg.liteAutoDelay = value })
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

// Skills 存储迁移弹窗
const migrateDialog = ref({ open: false, from: '', to: '', migrating: false })

function setSkillStorage(v: string) {
  if (v !== 'agentpack' && v !== 'unified') return
  if (v === settings.config.skillStorage) return
  // 弹窗确认后才执行迁移
  const from = settings.config.skillStorage === 'agentpack' ? '~/.agentpack/skills/' : '~/.agents/skills/'
  const to = v === 'agentpack' ? '~/.agentpack/skills/' : '~/.agents/skills/'
  migrateDialog.value = { open: true, from, to, migrating: false }
}

async function confirmMigrate() {
  const target = migrateDialog.value.to === '~/.agentpack/skills/' ? 'agentpack' : 'unified'
  migrateDialog.value.migrating = true
  try {
    const result = await api.skills.migrateStorage(target)
    if (result?.errors?.length) {
      throw new Error(result.errors.join('; '))
    }
    const count = result?.migrated ?? 0
    const next = cloneConfig()
    next.skillStorage = target as 'agentpack' | 'unified'
    await settings.update(next)
    toast.success(t('settings.skills.migrateDialog.success', { count }))
  } catch (e) {
    toast.error(t('settings.skills.migrateDialog.error', { error: String(e) }))
    const revert = migrateDialog.value.from === '~/.agentpack/skills/' ? 'agentpack' : 'unified'
    const rollback = cloneConfig()
    rollback.skillStorage = revert as 'agentpack' | 'unified'
    settings.setConfig(rollback)
  } finally {
    migrateDialog.value.migrating = false
    migrateDialog.value.open = false
  }
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
  try {
    const result = await api.system.checkUpdate()
    updateResult.value = result
    if (result.hasUpdate) {
      toast.success(t('settings.toast.foundNewVersion', { latest: result.latestVersion, current: result.currentVersion }), {
        duration: 5000,
      })
      events.emit('app:update-available', result)
    } else if (result.changelog) {
      toast.success(result.message || t('update.message.latest', { version: result.currentVersion }))
    } else {
      toast.info(result.message || t('update.message.latest', { version: result.currentVersion }))
    }
  } catch (e) {
    const apiError = ApiError.from(e)
    toast.error(t('settings.toast.checkUpdateFailed', { error: apiError.message }), { duration: 5000 })
  } finally {
    updateChecking.value = false
  }
}

// 打开全局 UpdateDialog 查看更新日志（无新版本时也可查看）
function openChangelog() {
  if (!updateResult.value) return
  events.emit(updateResult.value.hasUpdate ? 'app:update-available' : 'app:show-changelog', updateResult.value)
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
      { key: 'registry', label: 'Official', description: t('settings.market.officialDesc') },
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
    <div ref="scrollContainer" class="flex-1 overflow-y-auto">
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
        <CardTitle>{{ t('settings.lite.title') }}</CardTitle>
        <CardDescription>{{ t('settings.lite.desc') }}</CardDescription>
      </CardHeader>
      <CardContent class="space-y-3">
        <div class="flex items-center justify-between">
          <div>
            <Label>{{ t('settings.lite.autoLabel') }}</Label>
            <p class="text-xs text-muted-foreground">{{ t('settings.lite.autoHint') }}</p>
          </div>
          <Switch
            :model-value="settings.config.liteAutoEnabled ?? false"
            @update:model-value="setLiteAutoEnabled"
          />
        </div>
        <template v-if="settings.config.liteAutoEnabled">
          <Separator />
          <div class="flex items-center justify-between">
            <Label for="lite-delay">{{ t('settings.lite.delayLabel') }}</Label>
            <div class="flex items-center gap-2">
              <Input
                id="lite-delay"
                :model-value="String(settings.config.liteAutoDelay ?? 5)"
                type="number"
                :min="MIN_LITE_DELAY"
                :max="MAX_LITE_DELAY"
                class="w-20"
                @update:model-value="setLiteAutoDelay"
              />
              <span class="text-sm text-muted-foreground">{{ t('settings.lite.delayUnit') }}</span>
            </div>
          </div>
        </template>
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
        <!-- 来源回填已改为启动时后台自动执行，成功时由 App.vue 提示，此处不再提供手动按钮 -->
        <Separator />
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
            <Button v-if="updateResult?.changelog" variant="outline" size="sm" @click="openChangelog">
              <span>{{ t('settings.update.changelog') }}</span>
            </Button>
            <Button variant="outline" size="sm" :disabled="updateChecking" @click="checkUpdate">
              <PhArrowsClockwise :size="14" :class="{ 'animate-spin': updateChecking }" />
              <span>{{ updateChecking ? t('settings.update.checking') : t('settings.update.checkUpdate') }}</span>
            </Button>
          </div>
        </div>

        </CardContent>
    </Card>

    <!-- 导入确认弹窗 -->
    <Dialog v-model:open="importDialog.open" :scroll-root="scrollContainer">
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

    <!-- 更新日志弹窗统一由全局 UpdateDialog 组件承载（App.vue 挂载） -->

    <!-- Skills 存储迁移确认弹窗 -->
    <Dialog v-model:open="migrateDialog.open" :scroll-root="scrollContainer">
      <DialogContent class="max-w-md">
        <DialogHeader>
          <DialogTitle>{{ t('settings.skills.migrateDialog.title') }}</DialogTitle>
          <DialogDescription>{{ t('settings.skills.migrateDialog.desc') }}</DialogDescription>
        </DialogHeader>
        <div class="space-y-3 py-2">
          <div class="flex items-center gap-2 text-sm">
            <span class="text-muted-foreground">{{ t('settings.skills.migrateDialog.from') }}:</span>
            <code class="rounded bg-muted px-1.5 py-0.5 text-xs">{{ migrateDialog.from }}</code>
          </div>
          <div class="flex items-center gap-2 text-sm">
            <span class="text-muted-foreground">{{ t('settings.skills.migrateDialog.to') }}:</span>
            <code class="rounded bg-muted px-1.5 py-0.5 text-xs">{{ migrateDialog.to }}</code>
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" size="sm" @click="migrateDialog.open = false">{{ t('settings.skills.migrateDialog.cancelBtn') }}</Button>
          <Button size="sm" :disabled="migrateDialog.migrating" @click="confirmMigrate">
            {{ migrateDialog.migrating ? t('settings.skills.migrateDialog.migrating') : t('settings.skills.migrateDialog.migrateBtn') }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

      </div>
    </div>
  </div>
</template>
