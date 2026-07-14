<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, events, type UpdateCheckResult } from '@/lib/api'
import { useToast } from '@/composables/useToast'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter, Button } from '@/components/ui'
import { PhDownload, PhFolderOpen, PhArrowsClockwise } from '@phosphor-icons/vue'

function renderMarkdown(md: string): string {
  if (!md) return ''
  return DOMPurify.sanitize(marked.parse(md, { async: false }) as string)
}

const { t } = useI18n()
const toast = useToast()

const open = ref(false)
const result = ref<UpdateCheckResult | null>(null)
const downloadStatus = ref<'idle' | 'downloading' | 'complete' | 'error'>('idle')
const downloadProgress = ref(0)
const downloadSpeed = ref('')
const downloadedFilePath = ref('')
const downloadedFileName = ref('')

let offUpdateAvailable: (() => void) | null = null
let offDownloadProgress: (() => void) | null = null
let offDownloadComplete: (() => void) | null = null
let offDownloadError: (() => void) | null = null

onMounted(() => {
  offUpdateAvailable = events.on('app:update-available', (data: any) => {
    if (data && typeof data === 'object') {
      result.value = data as UpdateCheckResult
      open.value = true
    }
  })
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
      toast.error(data.message || t('settings.toast.downloadFailedMsg', { message: '' }))
    }
  })
})

onUnmounted(() => {
  offUpdateAvailable?.()
  offDownloadProgress?.()
  offDownloadComplete?.()
  offDownloadError?.()
})

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
  if (!result.value?.downloadUrl) return
  downloadStatus.value = 'downloading'
  downloadProgress.value = 0
  downloadSpeed.value = ''
  downloadedFilePath.value = ''
  downloadedFileName.value = ''
  try {
    await api.system.startDownloadUpdate(result.value.downloadUrl)
  } catch (e) {
    downloadStatus.value = 'error'
  }
}

async function cancelDownload() {
  try {
    await api.system.cancelDownload()
    downloadStatus.value = 'idle'
    downloadProgress.value = 0
    downloadSpeed.value = ''
  } catch {
  }
}

async function openDownloadedFile() {
  if (!downloadedFilePath.value) return
  try {
    await api.system.openDownloadedFile(downloadedFilePath.value)
  } catch (e) {
    toast.error(t('settings.toast.openFileFailed', { error: String(e) }))
  }
}

function handleClose() {
  if (downloadStatus.value === 'downloading') {
    cancelDownload()
  }
  downloadStatus.value = 'idle'
  downloadProgress.value = 0
  downloadSpeed.value = ''
  downloadedFilePath.value = ''
  downloadedFileName.value = ''
  result.value = null
}
</script>

<template>
  <Dialog v-model:open="open" @update:open="(v) => { if (!v) handleClose() }">
    <DialogContent class="max-w-2xl max-h-[80vh] flex flex-col">
      <DialogHeader>
        <DialogTitle>{{ t('settings.update.changelog') }}</DialogTitle>
        <DialogDescription v-if="result">
          v{{ result.currentVersion }} → v{{ result.latestVersion }}
        </DialogDescription>
      </DialogHeader>
      <div class="flex-1 overflow-y-auto">
        <div
          class="text-sm text-muted-foreground leading-relaxed max-w-none [&_a]:text-primary [&_a]:underline [&_h1]:text-base [&_h1]:font-semibold [&_h1]:mt-4 [&_h1]:mb-2 [&_h2]:text-sm [&_h2]:font-semibold [&_h2]:mt-3 [&_h2]:mb-2 [&_ul]:list-disc [&_ul]:pl-5 [&_ol]:list-decimal [&_ol]:pl-5 [&_li]:my-1 [&_pre]:bg-muted [&_pre]:p-3 [&_pre]:rounded [&_pre]:overflow-x-auto [&_code]:text-primary [&_hr]:my-4 [&_blockquote]:border-l-2 [&_blockquote]:border-border [&_blockquote]:pl-3 [&_blockquote]:italic"
          v-html="renderMarkdown(result?.changelog || '')"
        />
      </div>
      <DialogFooter class="flex-col sm:flex-col gap-2">
        <div v-if="downloadStatus === 'idle'" class="flex gap-2 justify-end">
          <Button size="sm" variant="default" @click="startDownload">
            <PhDownload :size="14" />
            <span>{{ t('settings.update.download') }}</span>
          </Button>
        </div>
        <div v-if="downloadStatus === 'downloading'" class="w-full space-y-1.5">
          <div class="flex items-center gap-2">
            <div class="h-2 flex-1 overflow-hidden rounded-full bg-muted">
              <div
                class="h-full rounded-full bg-primary transition-all duration-200"
                :style="{ width: downloadProgress + '%' }"
              />
            </div>
            <span class="w-14 text-right text-xs tabular-nums text-muted-foreground">{{ downloadProgress }}%</span>
          </div>
          <div class="flex items-center justify-end gap-2">
            <span v-if="downloadSpeed" class="text-xs text-muted-foreground">{{ downloadSpeed }}</span>
            <Button variant="outline" size="sm" class="h-auto px-1 text-xs text-destructive border-destructive/30" @click="cancelDownload">
              {{ t('settings.update.cancelDownload') }}
            </Button>
          </div>
        </div>
        <div v-if="downloadStatus === 'complete'" class="flex gap-2 justify-end">
          <Button size="sm" variant="default" @click="openDownloadedFile">
            <PhFolderOpen :size="14" />
            <span>{{ t('settings.update.openFile') }}</span>
          </Button>
        </div>
        <div v-if="downloadStatus === 'error'" class="flex gap-2 justify-end">
          <Button size="sm" variant="outline" @click="startDownload">
            <PhArrowsClockwise :size="14" />
            <span>{{ t('settings.update.retryDownload') }}</span>
          </Button>
        </div>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>