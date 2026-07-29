<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAgentsStore } from '@/stores/agents'
import { Card, CardContent, Badge, Spinner, Button, Switch, Empty, EmptyHeader, EmptyTitle, EmptyDescription, EmptyMedia } from '@/components/ui'
import { PhArrowsClockwise, PhCircleNotch } from '@phosphor-icons/vue'
import { ApiError } from '@/lib/api'
import { agentLogoUrl, agentLogoInvertClass, statusVariant, statusLabel, variantLabel, normalizeVariant } from '@/composables/useAgentHelpers'
import { useToast } from '@/composables/useToast'


const { t } = useI18n()
const agents = useAgentsStore()
const toast = useToast()

const detected = computed(() => agents.items.filter((a) => a.status !== 'not_found').length)
const enabled = computed(() => agents.items.filter((a) => a.status === 'enabled').length)

async function onRescan() {
  try {
    await agents.rescan()
    toast.success(t('agents.toast.scanComplete', { count: detected.value }))
  } catch (e) {
    const err = ApiError.from(e)
    toast.error(t('agents.toast.scanFailed', { error: err.message }))
  }
}

async function toggleAgent(id: string, val: boolean) {
  try {
    await agents.toggle(id, val)
  } catch (e) {
    const apiError = ApiError.from(e)
    toast.error(t('settings.toast.toggleAgentFailed', { error: apiError.message }))
  }
}
</script>

<template>
  <div class="flex h-full flex-col">
    <!-- 固定头部 -->
    <div class="shrink-0 border-b border-border px-8 pt-8 pb-4">
      <div class="mx-auto max-w-6xl">
        <div class="flex items-end justify-between">
          <div>
            <h1 class="text-2xl font-semibold tracking-tight">Agents</h1>
            <p class="mt-1 text-sm text-muted-foreground">
              {{ t('agents.subtitle') }}
            </p>
          </div>
          <div class="flex items-center gap-2">
            <Button variant="default" size="sm" :disabled="agents.loading" @click="onRescan">
              <PhArrowsClockwise v-if="!agents.loading" :size="14" />
              <Spinner v-else class="size-3.5" />
              <span>{{ t('agents.rescan') }}</span>
            </Button>
          </div>
        </div>

        <div class="mt-4 grid grid-cols-2 gap-3">
          <Card class="bg-card/50">
            <CardContent class="p-4">
              <div class="text-xs uppercase tracking-wider text-muted-foreground">{{ t('agents.detected') }}</div>
              <div class="mt-1 text-2xl font-semibold tabular-nums">{{ detected }}</div>
            </CardContent>
          </Card>
          <Card class="bg-card/50">
            <CardContent class="p-4">
              <div class="text-xs uppercase tracking-wider text-muted-foreground">{{ t('agents.enabled') }}</div>
              <div class="mt-1 text-2xl font-semibold tabular-nums text-success">{{ enabled }}</div>
            </CardContent>
          </Card>
        </div>
      </div>
    </div>

    <!-- 可滚动列表 -->
    <div class="flex-1 overflow-y-auto">
      <div class="mx-auto max-w-6xl px-8 py-4">
        <div v-if="agents.loading && agents.items.length === 0" class="flex items-center justify-center py-16">
          <Spinner class="size-5" />
        </div>

        <Empty v-else-if="agents.items.length === 0" class="mt-8">
          <EmptyMedia><PhCircleNotch :size="32" class="text-muted-foreground" /></EmptyMedia>
          <EmptyHeader>
            <EmptyTitle>{{ t('agents.emptyTitle') }}</EmptyTitle>
            <EmptyDescription>{{ t('agents.emptyDescription') }}</EmptyDescription>
          </EmptyHeader>
        </Empty>

        <div v-else class="space-y-2">
          <Card
            v-for="group in agents.variantGroups"
            :key="group.id"
          >
            <CardContent class="flex items-center gap-4 p-4">
              <div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-md bg-secondary p-2">
                <img :src="agentLogoUrl(group.id)" :alt="group.name" :class="['h-full w-full object-contain', agentLogoInvertClass(group.id)]" />
              </div>
              <div class="min-w-0 flex-1">
                <div class="flex items-center gap-2">
                  <h3 class="text-sm font-semibold">{{ group.name }}</h3>
                  <Badge variant="outline">{{ variantLabel(normalizeVariant(group.variant, group.id)) }}</Badge>
                  <Badge :variant="statusVariant(group.status)">{{ statusLabel(group.status) }}</Badge>
                </div>
                <p class="mt-0.5 truncate font-mono text-[11px] text-muted-foreground">
                  {{ group.configPath || t('agents.noConfigPath') }}
                </p>
              </div>
              <Switch
                :model-value="group.status === 'enabled'"
                :disabled="group.status === 'error' || group.status === 'not_found'"
                @update:model-value="(v) => toggleAgent(group.id, v)"
              />
            </CardContent>
          </Card>
        </div>

        <p v-if="agents.error" class="mt-4 text-xs text-destructive">
          {{ agents.error }}
        </p>

      </div>
    </div>
  </div>
</template>
