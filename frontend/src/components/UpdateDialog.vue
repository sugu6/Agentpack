<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, events, type UpdateCheckResult } from '@/lib/api'
import { useToast } from '@/composables/useToast'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter, Button } from '@/components/ui'
import { PhDownload, PhArrowsClockwise, PhPause, PhTrash } from '@phosphor-icons/vue'

const GITHUB_REPO = 'https://github.com/sugu6/Agentpack'

function renderMarkdown(md: string): string {
  if (!md) return ''
  // 将相对路径链接（如 ./CHANGELOG.md）转为 GitHub 绝对 URL
  const fixed = md.replace(/\]\(\.\/(CHANGELOG[^\)]*)\)/g, `](${GITHUB_REPO}/blob/master/$1)`)
  return DOMPurify.sanitize(marked.parse(fixed, { async: false }) as string)
}

// 拦截 changelog 内的链接点击，在系统浏览器打开而非 WebView 内
function onChangelogLinkClick(e: MouseEvent) {
  const target = e.target as HTMLElement
  const link = target.closest('a')
  if (!link?.href) return
  e.preventDefault()
  api.system.openUrl(link.href)
}

const { t } = useI18n()
const toast = useToast()

const open = ref(false)
const result = ref<UpdateCheckResult | null>(null)
const downloadStatus = ref<'idle' | 'downloading' | 'paused' | 'complete' | 'error'>('idle')
const downloadProgress = ref(0)
const downloadSpeed = ref('')
const downloadedBytes = ref(0)
const totalBytes = ref(0)
// 取消期间抑制后端 error 事件的弹窗，避免"已取消"与"下载失败"同时出现
const isCanceling = ref(false)
// 用于在后端未提供 speed 时本地推算速度
let lastTick = 0
let lastTickBytes = 0

let offUpdateAvailable: (() => void) | null = null
let offShowChangelog: (() => void) | null = null
let offProgress: (() => void) | null = null
let offPaused: (() => void) | null = null
let offComplete: (() => void) | null = null
let offError: (() => void) | null = null

onMounted(() => {
  // 检测到新版本时打开（App.vue 启动检查 / SettingsView 手动检查）
  offUpdateAvailable = events.on('app:update-available', (data: any) => {
    if (data && typeof data === 'object') {
      result.value = data as UpdateCheckResult
      resetDownloadState()
      open.value = true
    }
  })
  // 无新版本时查看更新日志（SettingsView 的"更新日志"按钮）
  offShowChangelog = events.on('app:show-changelog', (data: any) => {
    if (data && typeof data === 'object') {
      result.value = data as UpdateCheckResult
      resetDownloadState()
      open.value = true
    }
  })
  offProgress = events.on('update:download:progress', (data: any) => {
    if (data && typeof data === 'object') {
      downloadStatus.value = 'downloading'
      downloadProgress.value = Math.round(data.percent || 0)
      downloadedBytes.value = data.downloaded || 0
      totalBytes.value = data.total || 0
      downloadSpeed.value = formatSpeed(resolveSpeed(data))
    }
  })
  offPaused = events.on('update:download:paused', (data: any) => {
    if (data && typeof data === 'object') {
      downloadStatus.value = 'paused'
      downloadProgress.value = Math.round(data.percent || 0)
      downloadSpeed.value = ''
      downloadedBytes.value = data.downloaded || 0
      totalBytes.value = data.total || 0
      // 暂停期间无进度事件：重置本地速度推算基准，恢复后第一个
      // 进度包的字节差分不会把暂停时长计入（否则速度虚高/异常）
      lastTick = 0
      lastTickBytes = 0
    }
  })
  offComplete = events.on('update:download:complete', () => {
    downloadStatus.value = 'complete'
    downloadProgress.value = 100
    downloadSpeed.value = ''
  })
  offError = events.on('update:download:error', (data: any) => {
    if (isCanceling.value) return
    downloadStatus.value = 'error'
    downloadProgress.value = 0
    downloadSpeed.value = ''
    toast.error(t('settings.toast.downloadFailedMsg', {
      message: data?.message || t('settings.toast.unknownError'),
    }))
  })
})

onUnmounted(() => {
  offUpdateAvailable?.()
  offShowChangelog?.()
  offProgress?.()
  offPaused?.()
  offComplete?.()
  offError?.()
})

function resetDownloadState() {
  downloadStatus.value = 'idle'
  downloadProgress.value = 0
  downloadSpeed.value = ''
  downloadedBytes.value = 0
  totalBytes.value = 0
  lastTick = 0
  lastTickBytes = 0
}

// 后端 speed 字段偶发缺失/为 0（如首个进度包），此时用两次事件间的字节差自行推算
function resolveSpeed(data: any): number {
  const backendSpeed = Number(data?.speed)
  const now = Date.now()
  const bytes = Number(data?.downloaded) || 0
  let speed = Number.isFinite(backendSpeed) && backendSpeed > 0 ? backendSpeed : 0
  if (speed <= 0 && lastTick > 0 && now > lastTick) {
    speed = ((bytes - lastTickBytes) * 1000) / (now - lastTick)
  }
  lastTick = now
  lastTickBytes = bytes
  return speed > 0 ? speed : 0
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

function formatBytes(bytes: number): string {
  if (!bytes || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  return `${(bytes / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

async function startDownload() {
  if (!result.value?.downloadUrl) return
  downloadStatus.value = 'downloading'
  downloadProgress.value = 0
  downloadSpeed.value = ''
  try {
    await api.system.startDownloadUpdate(result.value.downloadUrl)
  } catch (e) {
    downloadStatus.value = 'error'
    toast.error(t('settings.toast.startDownloadFailed', { error: String(e) }))
  }
}

async function pauseDownload() {
  try {
    await api.system.pauseDownload()
  } catch {
    toast.error(t('settings.update.pauseFailed'))
  }
}

async function resumeDownload() {
  const prev = downloadStatus.value
  downloadStatus.value = 'downloading'
  // 与 paused 事件同步重置推算基准（事件可能先于本函数到达）
  lastTick = 0
  lastTickBytes = 0
  try {
    await api.system.resumeDownload()
  } catch {
    downloadStatus.value = prev
    toast.error(t('settings.update.resumeFailed'))
  }
}

// 取消下载 / 删除已暂停的下载，均清理临时文件
async function cancelDownload(notify = true) {
  isCanceling.value = true
  try {
    await api.system.cancelDownload()
    if (notify) toast.success(t('settings.update.downloadCancelled'))
  } catch {
    // 下载已结束等情况视为成功，静默处理
  } finally {
    resetDownloadState()
    isCanceling.value = false
  }
}

async function installUpdate() {
  try {
    await api.system.installUpdate()
  } catch (e) {
    // 展示具体原因（如"下载文件不是可执行的安装程序"），而非笼统的失败提示
    const err = e as { message?: unknown } | null
    const detail = err?.message ? String(err.message) : String(e)
    toast.error(t('settings.update.installFailed', { error: detail }))
  }
}

function handleClose() {
  // 下载中或已暂停时关闭弹窗：终止下载并清理临时文件
  if (downloadStatus.value === 'downloading' || downloadStatus.value === 'paused') {
    void cancelDownload(false)
  }
  resetDownloadState()
  result.value = null
}
</script>

<template>
  <Dialog v-model:open="open" @update:open="(v) => { if (!v) handleClose() }">
    <DialogContent class="max-w-2xl max-h-[80vh] flex flex-col">
      <DialogHeader>
        <div class="flex items-center justify-between gap-3 pr-8">
          <div class="min-w-0 shrink">
            <DialogTitle>{{ t('settings.update.changelog') }}</DialogTitle>
            <DialogDescription v-if="result">
              v{{ result.currentVersion }}
              <span v-if="result.hasUpdate"> → v{{ result.latestVersion }}</span>
            </DialogDescription>
          </div>
          <!-- 安装包名称：单行不换行，宽度不足时优先压缩左侧标题区 -->
          <span
            v-if="result?.downloadName"
            class="shrink-0 whitespace-nowrap font-mono text-xs text-muted-foreground"
            :title="result.downloadName"
          >
            {{ result.downloadName }}
          </span>
        </div>
      </DialogHeader>
      <div class="flex-1 overflow-y-auto">
        <div
          class="text-sm text-muted-foreground leading-relaxed max-w-none [&_a]:text-primary [&_a]:underline [&_h1]:text-base [&_h1]:font-semibold [&_h1]:mt-4 [&_h1]:mb-2 [&_h2]:text-sm [&_h2]:font-semibold [&_h2]:mt-3 [&_h2]:mb-2 [&_ul]:list-disc [&_ul]:pl-5 [&_ol]:list-decimal [&_ol]:pl-5 [&_li]:my-1 [&_pre]:bg-muted [&_pre]:p-3 [&_pre]:rounded [&_pre]:overflow-x-auto [&_code]:text-primary [&_hr]:my-4 [&_blockquote]:border-l-2 [&_blockquote]:border-border [&_blockquote]:pl-3 [&_blockquote]:italic"
          v-html="renderMarkdown(result?.changelog || '')"
          @click="onChangelogLinkClick"
        />
      </div>

      <!-- 下载进度：下载中与已暂停共用 -->
      <div v-if="downloadStatus === 'downloading' || downloadStatus === 'paused'" class="space-y-1.5 pt-1">
        <div class="flex items-center justify-between text-xs text-muted-foreground">
          <span>{{ formatBytes(downloadedBytes) }} / {{ formatBytes(totalBytes || result?.downloadSize || 0) }}</span>
          <span v-if="downloadStatus === 'paused'">{{ t('settings.update.pausedLabel') }}</span>
          <span v-else>{{ downloadSpeed || '—' }}</span>
        </div>
        <div class="flex items-center gap-2">
          <div class="h-2 flex-1 overflow-hidden rounded-full bg-muted">
            <div
              class="h-full rounded-full transition-all duration-200"
              :class="downloadStatus === 'paused' ? 'bg-muted-foreground/50' : 'bg-primary'"
              :style="{ width: downloadProgress + '%' }"
            />
          </div>
          <span class="w-10 text-right text-xs tabular-nums text-muted-foreground">{{ downloadProgress }}%</span>
        </div>
      </div>

      <!-- 下载完成：提示用户点击后重启以安装（会关闭应用并启动安装程序） -->
      <div v-if="downloadStatus === 'complete'" class="flex items-center gap-2 rounded-md border border-primary/30 bg-primary/5 px-3 py-2 text-xs text-primary">
        <PhDownload :size="14" class="shrink-0" />
        <span>{{ t('settings.update.downloadCompleteHint') }}</span>
      </div>

      <DialogFooter>
        <Button v-if="result?.releaseUrl" variant="outline" size="sm" @click="api.system.openUrl(result.releaseUrl)">
          <PhDownload :size="14" />
          <span>{{ t('settings.update.goToReleases') }}</span>
        </Button>

        <!-- 空闲：开始下载 -->
        <Button v-if="result?.hasUpdate && downloadStatus === 'idle'" size="sm" @click="startDownload">
          <PhDownload :size="14" />
          <span>{{ t('settings.update.download') }}</span>
        </Button>

        <!-- 下载中：暂停 -->
        <Button v-if="downloadStatus === 'downloading'" size="sm" variant="outline" @click="pauseDownload">
          <PhPause :size="14" />
          <span>{{ t('settings.update.pauseDownload') }}</span>
        </Button>

        <!-- 已暂停：继续下载在左，删除下载在右 -->
        <template v-if="downloadStatus === 'paused'">
          <Button size="sm" @click="resumeDownload">
            <PhDownload :size="14" />
            <span>{{ t('settings.update.resumeDownload') }}</span>
          </Button>
          <Button size="sm" variant="destructive" @click="cancelDownload()">
            <PhTrash :size="14" />
            <span>{{ t('settings.update.deleteDownload') }}</span>
          </Button>
        </template>

        <!-- 下载完成：手动确认安装（会退出应用） -->
        <Button v-if="downloadStatus === 'complete'" size="sm" @click="installUpdate">
          <PhDownload :size="14" />
          <span>{{ t('settings.update.installNow') }}</span>
        </Button>

        <!-- 失败：重试 -->
        <Button v-if="downloadStatus === 'error'" size="sm" variant="outline" @click="startDownload">
          <PhArrowsClockwise :size="14" />
          <span>{{ t('settings.update.retryDownload') }}</span>
        </Button>

        <Button variant="outline" size="sm" @click="open = false">
          <span>{{ t('common.close') }}</span>
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>