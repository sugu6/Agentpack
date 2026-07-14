<script setup lang="ts">
import { computed, ref } from 'vue'
import { PhDownloadSimple, PhTag, PhTerminal, PhCheck, PhTrash, PhInfo, PhGlobe, PhBookOpen, PhLink, PhStar } from '@phosphor-icons/vue'
import { Card, CardContent, CardHeader, CardTitle, CardDescription, Badge, Button, Spinner, Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter, Separator } from '@/components/ui'
import { useMarketStore } from '@/stores/market'
import { useMcpStore } from '@/stores/mcp'
import { useAgentSelector } from '@/composables/useAgentSelector'
import { agentLogoUrl, agentLogoInvertClass } from '@/composables/useAgentHelpers'
import { useConfirm } from '@/composables/useConfirm'
import { cn } from '@/lib/utils'
import { api, type MarketServer, ApiError } from '@/lib/api'
import { useToast } from '@/composables/useToast'
import { matchInstalledServer } from '@/lib/mcpMatch'

const props = defineProps<{ server: MarketServer }>()

const market = useMarketStore()
const mcpStore = useMcpStore()
const confirm = useConfirm()
const toast = useToast()
const { showDialog, selectedAgentIds, activeGroups, allAgentIds, allSelected, someSelected, isGroupSelected, toggleGroup, toggleSelectAll, openDialog } = useAgentSelector({ defaultAllSelected: false })
const busy = ref(false)
const uninstalling = ref(false)
const detailOpen = ref(false)

// 匹配已安装的 MCP 服务器：
// 1. source + sourceId 精确匹配（managed 安装路径）
// 2. command + args 归一化匹配（处理 cmd /c 包装与 @latest 差异，覆盖手动安装的 context7 等场景）
// 3. URL 匹配（http/sse）
// 4. sourceId 出现在 args 中的兜底匹配（smithery 搜索结果无 command 时）
const installedServer = computed(() => matchInstalledServer(props.server, mcpStore.items))
const installed = computed(() => !!installedServer.value)

const commandPreview = computed(() => {
  if (!props.server.command) return ''
  const parts = [props.server.command, ...(props.server.args || [])]
  return parts.join(' ')
})

// 详情 Dialog：完整的 command preview（含参数换行展示）
const commandFull = computed(() => {
  if (!props.server.command) return ''
  return [props.server.command, ...(props.server.args || [])].map((p, i) => i === 0 ? p : `  ${p}`).join(' \\\n')
})

const envKeys = computed(() => {
  const env = props.server.env || {}
  return Object.keys(env)
})

function openDetail() {
  detailOpen.value = true
}

async function openExternal(url: string) {
  if (!url) return
  await api.system.openUrl(url)
}

async function confirmInstall() {
  const ids = [...selectedAgentIds.value]
  if (ids.length === 0) return
  showDialog.value = false
  busy.value = true
  try {
    // Smithery 搜索结果不含 command/args，需先拉取详情获取 stdio/http 模板
    let serverToInstall = props.server
    if (props.server.source === 'smithery' && !props.server.command) {
      try {
        serverToInstall = await api.market.getServer('smithery', props.server.sourceId)
      } catch (e) {
        const apiError = ApiError.from(e)
        toast.error(`获取 Smithery 服务器详情失败：${apiError.message}`)
        busy.value = false
        return
      }
    }
    await market.installServer(serverToInstall, ids)
    await mcpStore.fetch()
    toast.success(`已安装 MCP 服务器 ${serverToInstall.title || serverToInstall.name}`)
  } catch (e) {
    const apiError = ApiError.from(e)
    toast.error(`安装失败：${apiError.message}`)
  } finally {
    busy.value = false
  }
}

async function uninstallServer() {
  if (!installedServer.value) return
  const ok = await confirm.confirm({
    title: '确认卸载',
    message: `确定要卸载 MCP 服务器 "${installedServer.value.name}" 吗？将从所有绑定的 Agent 配置中移除。`,
    confirmText: '卸载',
    variant: 'destructive',
  })
  if (!ok) return
  uninstalling.value = true
  try {
    await mcpStore.remove(installedServer.value.id)
    toast.success(`已卸载 MCP 服务器 ${installedServer.value.name}`)
  } catch (e) {
    const apiError = ApiError.from(e)
    toast.error(`MCP 卸载失败：${apiError.message}`)
  } finally {
    uninstalling.value = false
  }
}
</script>

<template>
  <Card :class="cn('group select-none transition-colors hover:border-primary/40', installed && 'border-emerald-500 !bg-emerald-500/10')">
    <CardHeader class="cursor-pointer" @click="openDetail">
      <div class="flex items-start justify-between gap-3">
        <div class="min-w-0 flex-1">
          <CardTitle class="flex items-center gap-2">
            <span class="truncate">{{ server.title || server.name }}</span>
            <span v-if="server.title" class="truncate font-mono text-xs font-normal text-muted-foreground">
              {{ server.name }}
            </span>
          </CardTitle>
          <CardDescription v-if="server.description" class="mt-1 line-clamp-2">
            {{ server.description }}
          </CardDescription>
        </div>
        <div class="flex shrink-0 items-center gap-1.5">
          <Badge variant="secondary" class="font-mono text-[10px]">
            {{ server.source }}
          </Badge>
          <button
            type="button"
            class="rounded-md p-1 text-muted-foreground opacity-0 transition-opacity hover:bg-muted hover:text-foreground group-hover:opacity-100"
            title="查看详情"
            @click.stop="openDetail"
          >
            <PhInfo :size="14" />
          </button>
        </div>
      </div>
    </CardHeader>

    <CardContent class="space-y-3">
      <div v-if="server.tags?.length" class="flex flex-wrap items-center gap-1.5">
        <PhTag :size="11" class="text-muted-foreground" />
        <Badge v-for="tag in (server.tags || []).slice(0, 4)" :key="tag" variant="outline" class="font-normal text-[10px]">
          {{ tag }}
        </Badge>
        <span v-if="(server.tags?.length || 0) > 4" class="text-[10px] text-muted-foreground">
          +{{ (server.tags?.length || 0) - 4 }}
        </span>
      </div>

      <div v-if="commandPreview" class="flex items-center gap-2 rounded-md bg-muted/40 px-2.5 py-1.5">
        <PhTerminal :size="13" class="shrink-0 text-muted-foreground" />
        <code class="truncate font-mono text-[11px]" :title="commandPreview">
          {{ commandPreview }}
        </code>
      </div>

      <div class="flex items-center justify-between pt-1">
        <p class="text-[11px] text-muted-foreground">
          {{ activeGroups.length }} 个 Agent
          <template v-if="server.installs">
            <span class="mx-1.5">·</span>
            <PhDownloadSimple :size="11" class="inline" />
            {{ server.installs.toLocaleString() }}
          </template>
        </p>
        <div class="flex items-center gap-2">
          <Button
            v-if="installed"
            size="sm"
            variant="outline"
            class="border-destructive/40 text-destructive hover:bg-destructive/10"
            :disabled="uninstalling"
            @click.stop="uninstallServer"
          >
            <Spinner v-if="uninstalling" class="mr-1" />
            <PhTrash v-else :size="13" class="mr-1 text-destructive" />
            卸载
          </Button>
          <Button
            v-else
            size="sm"
            variant="outline"
            :disabled="busy"
            @click.stop="openDialog"
          >
            <Spinner v-if="busy" class="mr-1" />
            <PhDownloadSimple v-else :size="13" class="mr-1" />
            安装
          </Button>
          <Badge v-if="installed" variant="outline" class="text-[10px] text-emerald-600 dark:text-emerald-400 border-emerald-500/40">
            <PhCheck :size="11" class="mr-0.5" />
            已安装
          </Badge>
        </div>
      </div>
    </CardContent>
  </Card>

  <!-- 详情对话框 -->
  <Dialog v-model:open="detailOpen">
    <DialogContent class="max-w-lg select-none">
      <DialogHeader>
        <DialogTitle class="flex items-center gap-2 pr-6">
          <span class="truncate">{{ server.title || server.name }}</span>
          <Badge variant="secondary" class="shrink-0 font-mono text-[10px]">
            {{ server.source }}
          </Badge>
          <Badge v-if="installed" variant="outline" class="shrink-0 text-[10px] text-emerald-600 dark:text-emerald-400 border-emerald-500/40">
            <PhCheck :size="11" class="mr-0.5" />
            已安装
          </Badge>
        </DialogTitle>
        <DialogDescription v-if="server.name !== server.title" class="font-mono text-xs">
          {{ server.name }}
        </DialogDescription>
      </DialogHeader>

      <div class="max-h-[60vh] space-y-4 overflow-y-auto py-2">
        <!-- 描述 -->
        <p v-if="server.description" class="text-sm leading-relaxed text-foreground">
          {{ server.description }}
        </p>

        <Separator v-if="server.tags?.length || commandFull || server.url || envKeys.length || server.installs || server.stars" />

        <!-- 标签 -->
        <div v-if="server.tags?.length" class="space-y-1.5">
          <p class="text-xs font-medium text-muted-foreground">标签</p>
          <div class="flex flex-wrap gap-1.5">
            <Badge v-for="tag in server.tags" :key="tag" variant="outline" class="font-normal text-[10px]">
              {{ tag }}
            </Badge>
          </div>
        </div>

        <!-- 命令预览 -->
        <div v-if="commandFull" class="space-y-1.5">
          <p class="text-xs font-medium text-muted-foreground">命令</p>
          <pre class="overflow-x-auto rounded-md bg-muted/60 px-3 py-2 font-mono text-[11px] leading-relaxed"><code>{{ commandFull }}</code></pre>
        </div>

        <!-- URL（http/sse） -->
        <div v-if="server.url" class="space-y-1.5">
          <p class="text-xs font-medium text-muted-foreground">URL</p>
          <div class="flex items-center gap-2">
            <PhLink :size="13" class="shrink-0 text-muted-foreground" />
            <code class="truncate font-mono text-[11px]">{{ server.url }}</code>
          </div>
        </div>

        <!-- Transport -->
        <div v-if="server.transport" class="flex items-center gap-2 text-xs">
          <span class="text-muted-foreground">Transport:</span>
          <Badge variant="outline" class="font-mono text-[10px]">{{ server.transport }}</Badge>
        </div>

        <!-- 环境变量 -->
        <div v-if="envKeys.length" class="space-y-1.5">
          <p class="text-xs font-medium text-muted-foreground">环境变量（安装时填写）</p>
          <div class="flex flex-wrap gap-1.5">
            <Badge v-for="key in envKeys" :key="key" variant="outline" class="font-mono text-[10px]">
              {{ key }}
            </Badge>
          </div>
        </div>

        <Separator v-if="server.homepage || server.docs || server.installs || server.stars" />

        <!-- 统计 -->
        <div v-if="server.installs || server.stars" class="flex flex-wrap items-center gap-4 text-xs text-muted-foreground">
          <span v-if="server.installs" class="inline-flex items-center gap-1">
            <PhDownloadSimple :size="13" />
            {{ server.installs.toLocaleString() }} 次安装
          </span>
          <span v-if="server.stars" class="inline-flex items-center gap-1">
            <PhStar :size="13" />
            {{ server.stars.toLocaleString() }} stars
          </span>
        </div>

        <!-- 外链 -->
        <div v-if="server.homepage || server.docs" class="flex flex-wrap gap-2">
          <Button v-if="server.homepage" size="sm" variant="outline" @click="openExternal(server.homepage)">
            <PhGlobe :size="13" class="mr-1.5" />
            主页
          </Button>
          <Button v-if="server.docs" size="sm" variant="outline" @click="openExternal(server.docs)">
            <PhBookOpen :size="13" class="mr-1.5" />
            文档
          </Button>
        </div>
      </div>

      <DialogFooter>
        <Button v-if="installed" variant="outline" class="border-destructive/40 text-destructive hover:bg-destructive/10" :disabled="uninstalling" @click="detailOpen = false; uninstallServer()">
          <Spinner v-if="uninstalling" class="mr-1" />
          <PhTrash v-else :size="13" class="mr-1 text-destructive" />
          卸载
        </Button>
        <Button v-else variant="outline" :disabled="busy" @click="detailOpen = false; openDialog()">
          <Spinner v-if="busy" class="mr-1" />
          <PhDownloadSimple v-else :size="13" class="mr-1" />
          安装
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>

  <!-- Agent 选择对话框 -->
  <Dialog v-model:open="showDialog">
    <DialogContent class="max-w-md select-none">
      <DialogHeader>
        <DialogTitle>选择目标 Agent</DialogTitle>
        <DialogDescription>
          选择要将 {{ server.title || server.name }} 安装到哪些 Agent。
        </DialogDescription>
      </DialogHeader>

      <div class="space-y-3 py-2">
        <label class="flex items-center gap-2 text-sm font-medium">
          <input
            type="checkbox"
            :checked="allSelected"
            :indeterminate="someSelected"
            @change="(e: Event) => toggleSelectAll((e.target as HTMLInputElement).checked)"
            class="h-4 w-4"
          />
          全选 / 取消
          <span v-if="selectedAgentIds.size > 0" class="ml-auto text-xs text-muted-foreground">
            {{ selectedAgentIds.size }}/{{ allAgentIds.length }} 已选
          </span>
        </label>

        <div class="flex flex-wrap gap-2">
          <button
            v-for="group in activeGroups"
            :key="group.id"
            type="button"
            class="inline-flex items-center gap-1.5 rounded-full border px-3 py-1.5 text-xs font-medium transition-colors"
            :class="isGroupSelected(group)
              ? 'border-primary bg-primary text-primary-foreground'
              : 'border-border bg-muted/40 text-muted-foreground hover:bg-muted'"
            @click="toggleGroup(group, !isGroupSelected(group))"
          >
            <img :src="agentLogoUrl(group.id)" :alt="group.name" :class="['h-3.5 w-3.5 object-contain', agentLogoInvertClass(group.id)]" />
            {{ group.name }}
            <span class="text-[10px] opacity-70">{{ group.ids.length }}</span>
          </button>
        </div>
      </div>

      <DialogFooter>
        <Button variant="ghost" @click="showDialog = false">取消</Button>
        <Button :disabled="selectedAgentIds.size === 0 || busy" @click="confirmInstall">
          <Spinner v-if="busy" class="mr-1" />
          安装到 {{ selectedAgentIds.size }} 个 Agent
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
