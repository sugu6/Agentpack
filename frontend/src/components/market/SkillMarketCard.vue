<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { PhDownloadSimple, PhGithubLogo, PhCheck, PhTrash } from '@phosphor-icons/vue'
import {
  Card, CardContent, CardHeader, CardTitle, CardDescription,
  Badge, Button, Spinner,
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter,
} from '@/components/ui'
import { useMarketStore } from '@/stores/market'
import { useSkillsStore } from '@/stores/skills'
import { useAgentSelector } from '@/composables/useAgentSelector'
import { agentLogoUrl, agentLogoInvertClass } from '@/composables/useAgentHelpers'
import { useConfirm } from '@/composables/useConfirm'
import { cn } from '@/lib/utils'
import type { MarketSkill } from '@/lib/api'
import { ApiError, api } from '@/lib/api'
import { useToast } from '@/composables/useToast'

const props = defineProps<{ skill: MarketSkill }>()

const { t } = useI18n()
const market = useMarketStore()
const skillsStore = useSkillsStore()
const confirm = useConfirm()
const toast = useToast()
const { showDialog, selectedAgentIds, activeGroups, allAgentIds, allSelected, someSelected, isGroupSelected, toggleGroup, toggleSelectAll, openDialog } = useAgentSelector({ defaultAllSelected: false })

const busy = ref(false)
const uninstalling = ref(false)
const installError = ref('')

// 匹配已安装的 skill
// 1. 有 repoOwner → 精确复合匹配（owner + repo + directory）
// 2. 无 repoOwner（TRAE 内置 / 手动安装）→ directory + name + description 全匹配
//    通过 description 区分不同仓库的同名 skill（如 test-driven-development 在多个仓库都有）
function normalizeDesc(s: string | undefined): string {
  return (s ?? '').trim().toLowerCase().replace(/\s+/g, ' ')
}
const installedSkill = computed(() =>
  skillsStore.skills.find(s => {
    if (s.directory !== props.skill.directory) return false
    if (s.repoOwner) {
      return s.repoOwner === props.skill.repoOwner && s.repoName === props.skill.repoName
    }
    // 无仓库来源：directory + name + description 全匹配
    return s.name === props.skill.name
      && normalizeDesc(s.description) === normalizeDesc(props.skill.description)
  }),
)
const installed = computed(() => !!installedSkill.value)

const repoUrl = computed(() =>
  `https://github.com/${props.skill.repoOwner}/${props.skill.repoName}`,
)

// 在系统默认浏览器中打开仓库链接
// Wails WebView2 中 <a target="_blank"> 不会触发系统浏览器，必须用 BrowserOpenURL
function openRepoUrl() {
  api.system.openUrl(repoUrl.value)
}

async function confirmInstall() {
  const ids = [...selectedAgentIds.value]
  if (ids.length === 0) return
  installError.value = ''
  busy.value = true
  try {
    await market.installSkill(props.skill, ids)
    showDialog.value = false
    await skillsStore.load()
    toast.success(t('skills.toast.installSuccess', { name: props.skill.name }))
  } catch (e) {
    const apiError = ApiError.from(e)
    installError.value = apiError.message
    toast.error(t('market.toast.installFailed', { error: apiError.message }))
  } finally {
    busy.value = false
  }
}

async function uninstallSkill() {
  if (!installedSkill.value) return
  const ok = await confirm.confirm({
    title: t('dialog.confirm.uninstall'),
    message: t('skills.uninstallConfirmMessageNamed', { name: installedSkill.value.name }),
    confirmText: t('common.uninstall'),
    variant: 'destructive',
  })
  if (!ok) return
  uninstalling.value = true
  try {
    await skillsStore.uninstall(installedSkill.value.id)
    toast.success(t('skills.toast.uninstalled', { name: installedSkill.value.name }))
  } catch (e) {
    const apiError = ApiError.from(e)
    toast.error(t('skills.toast.uninstallFailed', { error: apiError.message }))
  } finally {
    uninstalling.value = false
  }
}
</script>

<template>
  <Card :class="cn('select-none transition-colors hover:border-primary/40', installed && 'border-emerald-500 !bg-emerald-500/10')">
    <CardHeader>
      <div class="flex items-start justify-between gap-3">
        <div class="min-w-0 flex-1">
          <CardTitle class="flex items-center gap-2">
            <span class="truncate">{{ skill.name }}</span>
          </CardTitle>
          <CardDescription v-if="skill.description" class="mt-1 line-clamp-2">
            {{ skill.description }}
          </CardDescription>
        </div>
        <div class="flex shrink-0 items-center gap-1">
          <Badge
            v-if="skill.source === 'skills-sh'"
            variant="default"
            class="font-mono text-[10px]"
          >
            skills.sh
          </Badge>
          <Badge
            variant="secondary"
            class="inline-flex items-center gap-0.5 font-mono text-[10px]"
          >
            <PhGithubLogo :size="10" />
            GitHub
          </Badge>
        </div>
      </div>
    </CardHeader>

    <CardContent class="space-y-3">
      <div class="flex items-center gap-1.5 text-[11px] text-muted-foreground">
        <button
          type="button"
          class="truncate font-mono transition-colors hover:text-foreground hover:underline"
          :title="t('skills.viewRepo', { repo: `${skill.repoOwner}/${skill.repoName}` })"
          @click="openRepoUrl"
        >
          {{ skill.repoOwner }}/{{ skill.repoName }}
        </button>
        <template v-if="skill.installs > 0">
          <span class="text-border">·</span>
          <PhDownloadSimple :size="12" />
          <span>{{ t('market.installsCount', { count: skill.installs.toLocaleString() }) }}</span>
        </template>
      </div>

      <div class="flex items-center justify-between pt-1">
        <p class="text-[11px] text-muted-foreground">
          {{ t('common.agentCount', { count: activeGroups.length }) }}
        </p>
        <div class="flex items-center gap-2">
          <Button
            v-if="installed"
            size="sm"
            variant="outline"
            class="border-destructive/40 text-destructive hover:bg-destructive/10"
            :disabled="uninstalling"
            @click="uninstallSkill"
          >
            <Spinner v-if="uninstalling" class="mr-1" />
            <PhTrash v-else :size="13" class="mr-1 text-destructive" />
            {{ t('common.uninstall') }}
          </Button>
          <Button
            v-else
            size="sm"
            variant="outline"
            :disabled="busy"
            @click="openDialog"
          >
            <Spinner v-if="busy" class="mr-1" />
            <PhDownloadSimple v-else :size="13" class="mr-1" />
            {{ t('common.install') }}
          </Button>
          <Badge v-if="installed" variant="outline" class="text-[10px] text-emerald-600 dark:text-emerald-400 border-emerald-500/40">
            <PhCheck :size="11" class="mr-0.5" />
            {{ t('common.installed') }}
          </Badge>
        </div>
      </div>
    </CardContent>
  </Card>

  <!-- Agent 选择对话框（默认全不选） -->
  <Dialog v-model:open="showDialog">
    <DialogContent class="max-w-md select-none">
      <DialogHeader>
        <DialogTitle>{{ t('skills.selectTargetAgentsTitle') }}</DialogTitle>
        <DialogDescription>
          {{ t('skills.marketInstallTargetDesc', { name: skill.name }) }}
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
          {{ t('common.toggleAll') }}
          <span v-if="selectedAgentIds.size > 0" class="ml-auto text-xs text-muted-foreground">
            {{ selectedAgentIds.size }}/{{ allAgentIds.length }} {{ t('common.selected') }}
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
        <div v-if="installError" class="mb-3 w-full text-xs text-destructive">
          {{ installError }}
        </div>
        <Button variant="ghost" :disabled="busy" @click="showDialog = false">{{ t('common.cancel') }}</Button>
        <Button :disabled="selectedAgentIds.size === 0 || busy" @click="confirmInstall">
          <Spinner v-if="busy" class="mr-1" />
          {{ t('market.installToAgents', { count: selectedAgentIds.size }) }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
