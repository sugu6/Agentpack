<script setup lang="ts">
import { computed, onActivated, onDeactivated, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { PhStorefront, PhMagnifyingGlass, PhBooks, PhSparkle } from '@phosphor-icons/vue'
import { Button, Input, Spinner, Tabs, TabsList, TabsTrigger, TabsContent, Empty, EmptyHeader, EmptyTitle, EmptyDescription } from '@/components/ui'
import MarketCard from '@/components/market/MarketCard.vue'
import SkillMarketCard from '@/components/market/SkillMarketCard.vue'
import LoadMore from '@/components/common/LoadMore.vue'
import { useMarketStore } from '@/stores/market'
import { useSettingsStore } from '@/stores/settings'
import { useSkillsStore } from '@/stores/skills'
import { useMcpStore } from '@/stores/mcp'
import { events } from '@/lib/api'
import type { MarketServer } from '@/lib/api'
import { transportLabel } from '@/lib/utils'

const PAGE_SIZE = 30

const { t } = useI18n()
const market = useMarketStore()
const settings = useSettingsStore()
const skillsStore = useSkillsStore()
const mcpStore = useMcpStore()

const query = ref('')
const skillQuery = ref('')
const transportFilter = ref<string>('')
const mode = ref<'servers' | 'skills'>('servers')
const skillSource = ref<'github' | 'skills-sh'>('github')
const mounted = ref(true)
const initialized = ref(false)
const skillsSearched = ref(false)
// 搜索按钮加载状态（含最小显示时间，避免太快看不到 spinner）
const searching = ref(false)
let unsubscribeReposChanged: (() => void) | undefined
let unsubscribeMcpChanged: (() => void) | undefined

// 滚动容器,作为各 tab 内 LoadMore 组件 IntersectionObserver 的 root
const scrollContainer = ref<HTMLElement | null>(null)

const total = computed(() => market.servers.total)
const skillTotal = computed(() => market.skills.total)

// 固定展示所有已知 transport 类型,避免因首页分页未加载到某类型(如 streamable-http)而隐藏筛选选项
const transportOptions = ['stdio', 'sse', 'streamable-http']

// transport 筛选在前端做(从已加载的 items 累积过滤)。
// registry API 不支持 transport 筛选参数,后端只做 API cursor 分页,
// 前端从已加载的所有 items 里按 transport 过滤,随 loadMore 加载更多数据而逐渐完整。
const filteredServers = computed<MarketServer[]>(() => {
  const transport = transportFilter.value
  if (!transport) return market.servers.items
  return market.servers.items.filter((s) => {
    const t = s.transport || 'stdio'
    return t === transport
  })
})

// hasResults 基于 API 返回的 items(不是筛选后的),用于控制空状态和 LoadMore 显示。
// 这样 transport 筛选后即使无结果,只要 API 有数据,LoadMore 仍会显示,用户可加载更多。
const hasResults = computed(() => market.servers.items.length > 0)
const hasSkillResults = computed(() => market.skills.items.length > 0)
const isSourceEnabled = (key: string) => computed(() => settings.config.marketSources?.[key]?.enabled !== false)
const registryEnabled = isSourceEnabled('registry')
const skillsShEnabled = isSourceEnabled('skills-sh')
const githubEnabled = isSourceEnabled('github')
const skillsSourceEnabled = computed(() => skillsShEnabled.value || githubEnabled.value)
const mcpSourceAvailable = registryEnabled

// 已加载条数(用于 LoadMore 进度显示)
// serversLoaded 显示筛选后的数量(用户实际看到的),total 是 API 返回的总数
const serversLoaded = computed(() => filteredServers.value.length)
const skillsLoaded = computed(() => market.skills.items.length)

onMounted(() => {
  ensureInit()
})

// KeepAlive 下用 onActivated/onDeactivated 管理事件订阅，避免缓存视图与 App.vue 双重处理
onActivated(() => {
  mounted.value = true
  unsubscribeReposChanged = events.on('skills:repos-changed', onReposChanged)
  unsubscribeMcpChanged = events.on('mcp:changed', onMcpChanged)
  // 若首次初始化在异步等待中被 deactivate 打断，这里补跑，避免列表一直为空
  ensureInit()
})

onDeactivated(() => {
  mounted.value = false
  unsubscribeReposChanged?.()
  unsubscribeMcpChanged?.()
})

// 单飞初始化：onMounted 与 onActivated 都可能触发，避免并发重复搜索；
// initView 内任何检查 mounted 失败（视图被切走）都不会置 initialized，
// 恢复激活时会通过 ensureInit 重新执行完整初始化。
let initPromise: Promise<void> | null = null
function ensureInit(): Promise<void> {
  if (initialized.value) return Promise.resolve()
  if (!initPromise) {
    initPromise = initView().finally(() => { initPromise = null })
  }
  return initPromise
}

async function initView(): Promise<void> {
  await settings.ensureLoaded()
  if (!mounted.value) return
  if (settings.isSkillReposChanged()) {
    await onReposChanged()
  }
  if (!githubEnabled.value && skillsShEnabled.value) {
    skillSource.value = 'skills-sh'
  }
  // 初始加载 MCP 服务器列表(API cursor 分页,transport 筛选由前端 computed 过滤)
  if (mcpSourceAvailable.value) {
    await market.search('registry', '', '', PAGE_SIZE)
  }
  // MCP 刷新在后台执行，不阻塞页面渲染
  mcpStore.refresh()
  // Skills 已安装列表必须等加载完成
  await skillsStore.reload()
  initialized.value = true
}

async function onSearch() {
  if (!mcpSourceAvailable.value) return
  if (searching.value) return
  query.value = query.value.trim()
  searching.value = true
  try {
    // 最小加载显示 300ms，确保 spinner 可见，避免搜索太快无反馈
    await Promise.all([
      market.search('registry', query.value, '', PAGE_SIZE),
      new Promise(resolve => setTimeout(resolve, 300)),
    ])
  } finally {
    searching.value = false
  }
}

// 搜索框清空时恢复全部列表,不走 onSearch(不显示搜索按钮 spinner)。
// 优先恢复本地缓存的 baseServers(首页+已 loadMore 的),瞬时完成,无需等待 API。
// 仅当 baseServers 为空(未加载过首页)时才调 API 拉取。
function restoreServersIfEmpty() {
  if (!mcpSourceAvailable.value) return
  if (market.currentQuery === '') return
  if (!market.restoreBaseServers()) {
    market.search('registry', '', '', PAGE_SIZE)
  }
}

// 搜索框清空时恢复全部列表(替代原生 DOM 监听,避免 KeepAlive 下元素重建导致监听失效)
watch(query, (val) => {
  if (val === '') restoreServersIfEmpty()
})

// transport 筛选由前端 computed 过滤,切换时不需要调 API,瞬时无 loading。
// 随 loadMore 加载更多数据,筛选结果会逐渐完整。
// 注意:hasMore 基于 API 的 hasMore(不是筛选后的),所以即使筛选后结果很少,LoadMore 仍会继续加载。

async function searchGithubSkills() {
  if (skillSource.value !== 'github' || !skillsSourceEnabled.value) return
  skillsSearched.value = true
  await market.searchSkills('', PAGE_SIZE, skillSource.value)
}

async function onReposChanged() {
  await searchGithubSkills()
}

function onMcpChanged() {
  mcpStore.refresh()
}

watch(mode, (newMode) => {
  if (newMode === 'skills') {
    searchGithubSkills()
  }
})

async function switchSkillSource(src: 'github' | 'skills-sh') {
  if (skillSource.value === src) return
  skillSource.value = src
  skillQuery.value = ''
  if (src === 'github') {
    skillsSearched.value = true
    await market.searchSkills('', PAGE_SIZE, src)
  } else {
    skillsSearched.value = false
    market.clearSkills()
  }
}

// 根据当前 skillSource 返回 skills-sh 或 github 对应的 i18n key 的翻译值
function sk(shKey: string, ghKey: string): string {
  return t(skillSource.value === 'skills-sh' ? shKey : ghKey)
}

async function onSkillSearch() {
  if (!skillsSourceEnabled.value) return
  const q = skillQuery.value.trim()
  if (q.length > 0 && q.length < 2) return
  skillsSearched.value = true
  await market.searchSkills(q, PAGE_SIZE, skillSource.value)
}
</script>

<template>
  <div class="flex h-full flex-col">
    <div class="shrink-0 border-b border-border px-8 pt-8 pb-4">
      <div class="mx-auto max-w-6xl">
        <h1 class="flex items-center gap-2 text-2xl font-semibold tracking-tight">
          <PhStorefront :size="22" weight="duotone" class="text-blue-500" />
          {{ t('market.title') }}
        </h1>
        <p class="mt-1 text-sm text-muted-foreground">
          {{ t('market.subtitle') }}
        </p>
      </div>
    </div>

    <div ref="scrollContainer" class="market-scroll-container flex-1 overflow-y-auto">
      <div class="mx-auto max-w-6xl px-8 py-4">
        <Tabs v-model="mode" class="space-y-6">
          <TabsList>
            <TabsTrigger value="servers" :disabled="!mcpSourceAvailable">
              <PhBooks :size="13" class="mr-1.5" />
              {{ t('market.mcpServers') }}
              <span v-if="total > 0" class="ml-1.5 text-[10px] text-muted-foreground">{{ total }}</span>
            </TabsTrigger>
            <TabsTrigger value="skills" :disabled="!skillsSourceEnabled">
              <PhSparkle :size="13" class="mr-1.5" />
              Skills
              <span v-if="skillTotal > 0" class="ml-1.5 text-[10px] text-muted-foreground">{{ skillTotal }}</span>
            </TabsTrigger>
          </TabsList>

          <!-- MCP 服务器 Tab -->
          <TabsContent value="servers" class="space-y-4">
            <Empty
              v-if="!mcpSourceAvailable"
            >
              <EmptyHeader>
                <EmptyTitle>{{ t('market.noMcpSource') }}</EmptyTitle>
                <EmptyDescription>{{ t('market.noMcpSourceDesc') }}</EmptyDescription>
              </EmptyHeader>
            </Empty>
            <template v-else>
              <form class="flex items-center gap-2" @submit.prevent="onSearch">
                <div class="relative flex-1">
                  <PhMagnifyingGlass :size="14" class="absolute left-3 top-1/2 -translate-y-1/2 text-muted-foreground" />
                  <Input
                    v-model="query"
                    :placeholder="t('market.searchMcpPlaceholder')"
                    class="pl-9"
                    :aria-label="t('market.searchMcpAria')"
                  />
                </div>
                <Button type="submit" :disabled="searching">
                  <Spinner v-if="searching" class="size-3.5" />
                  <PhMagnifyingGlass v-else :size="14" />
                  {{ t('common.search') }}
                </Button>
              </form>

              <!-- Transport 筛选标签 -->
              <div v-if="transportOptions.length > 0" class="flex items-center gap-1.5">
                <span class="text-xs text-muted-foreground">{{ t('market.transportLabel') }}:</span>
                <button
                  :aria-pressed="transportFilter === ''"
                  class="rounded-md px-2.5 py-1 text-[11px] font-medium transition-all duration-200"
                  :class="transportFilter === '' ? 'bg-primary text-primary-foreground shadow' : 'bg-muted/60 text-muted-foreground hover:bg-muted'"
                  @click="transportFilter = ''"
                >
                  {{ t('common.all') }}
                </button>
                <button
                  v-for="opt in transportOptions"
                  :key="opt"
                  :aria-pressed="transportFilter === opt"
                  class="rounded-md px-2.5 py-1 text-[11px] font-medium transition-all duration-200"
                  :class="transportFilter === opt ? 'bg-primary text-primary-foreground shadow' : 'bg-muted/60 text-muted-foreground hover:bg-muted'"
                  @click="transportFilter = opt"
                >
                  {{ transportLabel(opt) }}
                </button>
              </div>

              <div v-if="market.loadingServers && !hasResults" class="flex flex-col items-center justify-center gap-2 py-12 text-center">
                <Spinner />
                <p class="text-sm text-muted-foreground">{{ t('common.loading') }}</p>
              </div>

              <Empty
                v-else-if="!hasResults"
              >
                <EmptyHeader>
                  <EmptyTitle>{{ t('market.noServers') }}</EmptyTitle>
                  <EmptyDescription>{{ t('market.noServersDesc') }}</EmptyDescription>
                </EmptyHeader>
              </Empty>

              <div v-else class="grid grid-cols-1 gap-4 lg:grid-cols-2 transition-opacity duration-200" :class="{ 'opacity-60 pointer-events-none': market.loadingServers && !hasResults }">
                <MarketCard
                  v-for="server in filteredServers"
                  :key="server.sourceId"
                  :server="server"
                />
              </div>
            </template>
          </TabsContent>

          <!-- Skills Tab -->
          <TabsContent value="skills" class="space-y-4">
            <Empty
              v-if="!skillsSourceEnabled"
            >
              <EmptyHeader>
                <EmptyTitle>{{ t('market.skillsSourceDisabled') }}</EmptyTitle>
                <EmptyDescription>{{ t('market.skillsSourceDisabledDesc') }}</EmptyDescription>
              </EmptyHeader>
            </Empty>
            <template v-else>
              <!-- Skills 来源切换 -->
              <div class="flex items-center gap-2">
                <button
                  v-if="githubEnabled"
                  :aria-pressed="skillSource === 'github'"
                  class="rounded-md px-3 py-1.5 text-xs font-medium transition-all duration-200"
                  :class="skillSource === 'github' ? 'bg-primary text-primary-foreground shadow' : 'bg-muted/60 text-muted-foreground hover:bg-muted'"
                  @click="switchSkillSource('github')"
                >
                  {{ t('market.githubRepos') }}
                </button>
                <button
                  v-if="skillsShEnabled"
                  :aria-pressed="skillSource === 'skills-sh'"
                  class="rounded-md px-3 py-1.5 text-xs font-medium transition-all duration-200"
                  :class="skillSource === 'skills-sh' ? 'bg-primary text-primary-foreground shadow' : 'bg-muted/60 text-muted-foreground hover:bg-muted'"
                  @click="switchSkillSource('skills-sh')"
                >
                  skills.sh
                </button>
              </div>

              <form class="flex items-center gap-2" @submit.prevent="onSkillSearch">
                <div class="relative flex-1">
                  <PhMagnifyingGlass :size="14" class="absolute left-3 top-1/2 -translate-y-1/2 text-muted-foreground" />
                  <Input
                    v-model="skillQuery"
                    :placeholder="sk('market.searchSkillsShPlaceholder', 'market.searchGithubSkillsPlaceholder')"
                    class="pl-9"
                    :aria-label="t('market.searchSkillsAria')"
                  />
                </div>
                <Button type="submit" :disabled="market.loadingSkills">
                  <PhMagnifyingGlass :size="14" />
                  {{ t('common.search') }}
                </Button>
              </form>

              <p v-if="skillQuery.trim().length > 0 && skillQuery.trim().length < 2" class="text-xs text-muted-foreground">
                {{ t('market.skillsShMinLength') }}
              </p>

              <div v-if="market.loadingSkills && !hasSkillResults" class="flex flex-col items-center justify-center gap-2 py-12 text-center">
                <Spinner />
                <p class="text-sm text-muted-foreground">{{ t('common.loading') }}</p>
              </div>

              <Empty
                v-else-if="!skillsSearched"
              >
                <EmptyHeader>
                  <EmptyTitle>{{ sk('market.searchSkillsShPrompt', 'market.searchGithubPrompt') }}</EmptyTitle>
                  <EmptyDescription>{{ sk('market.searchSkillsShPromptDesc', 'market.searchGithubPromptDesc') }}</EmptyDescription>
                </EmptyHeader>
              </Empty>

              <Empty
                v-else-if="!hasSkillResults"
              >
                <EmptyHeader>
                  <EmptyTitle>{{ sk('market.noSkillsShSkills', 'market.noGithubSkills') }}</EmptyTitle>
                  <EmptyDescription>{{ sk('market.noSkillsShSkillsDesc', 'market.noGithubSkillsDesc') }}</EmptyDescription>
                </EmptyHeader>
              </Empty>

              <div v-else class="grid grid-cols-1 gap-4 lg:grid-cols-2">
                <SkillMarketCard
                  v-for="skill in market.skills.items"
                  :key="skill.id"
                  :skill="skill"
                />
              </div>
            </template>
          </TabsContent>
        </Tabs>

        <!-- LoadMore 统一放在 Tabs 外,根据当前 tab 切换,避免每个 TabsContent 重复 -->
        <!-- Transport 筛选时不显示 API total:registry 不支持按 transport 统计,
             显示 "2 / 2000" 会误导用户以为还有 1998 条 SSE。total=0 时 LoadMore
             隐藏进度文字,仅显示 "加载更多" 或无提示。 -->
        <LoadMore
          v-if="mode === 'servers' && hasResults"
          :loading="market.loadingServers"
          :has-more="market.servers.hasMore"
          :loaded="serversLoaded"
          :total="transportFilter ? 0 : market.servers.total"
          :scroll-root="scrollContainer"
          @load-more="market.loadMore()"
        />
        <LoadMore
          v-if="mode === 'skills' && hasSkillResults"
          :loading="market.loadingSkills"
          :has-more="market.skills.hasMore"
          :loaded="skillsLoaded"
          :total="market.skills.total"
          :scroll-root="scrollContainer"
          @load-more="market.loadMoreSkills()"
        />

        <p v-if="market.error" class="mt-4 text-xs text-destructive">
          {{ market.error }}
        </p>
      </div>
    </div>
  </div>
</template>
