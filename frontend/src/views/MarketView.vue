<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { PhStorefront, PhMagnifyingGlass, PhBooks, PhSparkle, PhFunnel } from '@phosphor-icons/vue'
import { Button, Input, Spinner, Tabs, TabsList, TabsTrigger, TabsContent, Empty, EmptyHeader, EmptyTitle, EmptyDescription, Select, SelectTrigger, SelectValue, SelectContent, SelectItem, Checkbox } from '@/components/ui'
import MarketCard from '@/components/market/MarketCard.vue'
import SkillMarketCard from '@/components/market/SkillMarketCard.vue'
import { useMarketStore } from '@/stores/market'
import { useSettingsStore } from '@/stores/settings'
import { useSkillsStore } from '@/stores/skills'
import { useMcpStore } from '@/stores/mcp'
import { events } from '@/lib/api'
import type { MarketServer } from '@/lib/api'

const PAGE_SIZE = 30
const ALL_VALUE = '__all__'

// Smithery 分类预设（来自 smithery.ai/servers 顶部 Categories）
// 每个分类对应一个语义搜索 query，选中时触发新搜索替换当前 query
const SMITHERY_CATEGORIES: { label: string; query: string }[] = [
  { label: '全部', query: '' },
  { label: 'Web 搜索', query: 'search the web for information' },
  { label: '浏览器自动化', query: 'automate and control web browsers' },
  { label: '学术研究', query: 'research papers citations and scholarly' },
  { label: '金融', query: 'financial data stocks and trading' },
  { label: '推理', query: 'thinking reasoning and problem solving' },
  { label: '开发工具', query: 'software development and coding tools' },
]

const market = useMarketStore()
const settings = useSettingsStore()
const skillsStore = useSkillsStore()
const mcpStore = useMcpStore()

const query = ref('')
const skillQuery = ref('')
const mode = ref<'servers' | 'skills'>('servers')
const mcpSource = ref<'official' | 'smithery'>('official')
const skillSource = ref<'github' | 'skills-sh'>('github')
const mounted = ref(true)
const skillsSearched = ref(false)
let unsubscribeReposChanged: (() => void) | undefined
let unsubscribeMcpChanged: (() => void) | undefined

// 筛选状态
// Smithery：toggle 通过拼接 is:xxx query 语法发送服务端过滤（与分类 query 组合）
const smitheryFilter = ref<{ category: string; deployed: boolean; verified: boolean; bySmithery: boolean }>({
  category: ALL_VALUE, deployed: false, verified: false, bySmithery: false,
})
// Official：API 不支持 registry/transport 服务端过滤，仅客户端过滤已加载结果
const officialFilter = ref<{ registry: string; transport: string }>({
  registry: ALL_VALUE, transport: ALL_VALUE,
})

// 构造 Smithery 完整 query（分类 query + toggle 的 is:xxx 语法）
function buildSmitheryQuery(): string {
  const parts: string[] = []
  const categoryQuery = smitheryFilter.value.category === ALL_VALUE ? '' : smitheryFilter.value.category
  if (categoryQuery) parts.push(categoryQuery)
  if (smitheryFilter.value.bySmithery) parts.push('is:smithery-managed')
  if (smitheryFilter.value.deployed) parts.push('is:deployed')
  if (smitheryFilter.value.verified) parts.push('is:verified')
  // 用户输入的搜索词（如果有且与分类不冲突）
  const userInput = query.value.trim()
  if (userInput && userInput !== categoryQuery) {
    parts.unshift(userInput)
  }
  return parts.join(' ')
}

// 无限滚动哨兵
const scrollContainer = ref<HTMLElement | null>(null)
const sentinel = ref<HTMLElement | null>(null)
let sentinelObserver: IntersectionObserver | null = null

const total = computed(() => market.servers.total)
const skillTotal = computed(() => market.skills.total)

// 筛选选项（从已加载结果动态提取）
const officialRegistries = computed(() => {
  const set = new Set<string>()
  for (const s of market.servers.items) {
    if (s.registry) set.add(s.registry)
  }
  return [...set].sort()
})

// 应用筛选后的服务器列表
// Smithery：toggle 已通过 query 语法发送服务端过滤，此处直接返回 items
// Official：API 不支持服务端过滤，此处仅客户端过滤已加载结果
const filteredServers = computed<MarketServer[]>(() => {
  const items = market.servers.items
  if (mcpSource.value === 'official') {
    const f = officialFilter.value
    return items.filter(s =>
      (!f.registry || f.registry === ALL_VALUE || s.registry === f.registry)
      && (!f.transport || f.transport === ALL_VALUE || s.transport === f.transport),
    )
  }
  return items
})

const hasResults = computed(() => filteredServers.value.length > 0)
const hasSkillResults = computed(() => market.skills.items.length > 0)
const isSourceEnabled = (key: string) => computed(() => settings.config.marketSources?.[key]?.enabled !== false)
const officialEnabled = isSourceEnabled('official')
const smitheryEnabled = isSourceEnabled('smithery')
const skillsShEnabled = isSourceEnabled('skills-sh')
const githubEnabled = isSourceEnabled('github')
const skillsSourceEnabled = computed(() => skillsShEnabled.value || githubEnabled.value)
const mcpSourceAvailable = computed(() =>
  mcpSource.value === 'official' ? officialEnabled.value : smitheryEnabled.value,
)

// 当前 tab 是否还有更多数据可加载（用于无限滚动分发）
const currentHasMore = computed(() =>
  mode.value === 'skills' ? market.skills.hasMore : market.servers.hasMore,
)
const currentLoading = computed(() =>
  mode.value === 'skills' ? market.loadingSkills : market.loadingServers,
)

onMounted(async () => {
  await settings.ensureLoaded()
  if (!mounted.value) return
  unsubscribeReposChanged = events.on('skills:repos-changed', onReposChanged)
  if (settings.isSkillReposChanged()) {
    onReposChanged()
  }
  unsubscribeMcpChanged = events.on('mcp:changed', onMcpChanged)
  if (!officialEnabled.value && smitheryEnabled.value) {
    mcpSource.value = 'smithery'
  }
  if (!githubEnabled.value && skillsShEnabled.value) {
    skillSource.value = 'skills-sh'
  }
  if (mcpSourceAvailable.value) {
    await market.search(mcpSource.value, '', '', PAGE_SIZE)
  }
  mcpStore.refresh()
  await skillsStore.reload()
  // 绑定无限滚动
  bindSentinel()
})

onUnmounted(() => {
  mounted.value = false
  sentinelObserver?.disconnect()
  sentinelObserver = null
  unsubscribeReposChanged?.()
  unsubscribeMcpChanged?.()
})

function bindSentinel() {
  if (!scrollContainer.value) return
  sentinelObserver = new IntersectionObserver(
    ([entry]) => {
      if (!entry.isIntersecting) return
      // 按 mode 分发到对应的 loadMore
      if (mode.value === 'skills') {
        if (market.skills.hasMore && !market.loadingSkills) {
          market.loadMoreSkills()
        }
      } else {
        if (market.servers.hasMore && !market.loadingServers) {
          market.loadMore()
        }
      }
    },
    { root: scrollContainer.value, rootMargin: '0px 0px 300px 0px' },
  )
  if (sentinel.value) sentinelObserver.observe(sentinel.value)
}

function searchGithubSkills() {
  if (skillSource.value !== 'github' || !skillsSourceEnabled.value) return
  skillsSearched.value = true
  market.searchSkills('', PAGE_SIZE, skillSource.value)
}

function onReposChanged() {
  searchGithubSkills()
}

function onMcpChanged() {
  mcpStore.refresh()
}

watch(mode, (newMode) => {
  if (newMode === 'skills') {
    searchGithubSkills()
  }
})

async function onSearch() {
  if (!mcpSourceAvailable.value) return
  if (mcpSource.value === 'smithery') {
    // Smithery：用 buildSmitheryQuery 组合用户输入 + toggle
    // 用户手动输入时重置分类（避免与预设 query 冲突），用 silent 跳过 watch 触发的重复搜索
    smitheryCategorySilent = true
    smitheryFilter.value.category = ALL_VALUE
    query.value = query.value.trim()
    await market.search(mcpSource.value, buildSmitheryQuery(), '', PAGE_SIZE)
  } else {
    await market.search(mcpSource.value, query.value.trim(), '', PAGE_SIZE)
  }
}

async function switchMcpSource(src: 'official' | 'smithery') {
  if (mcpSource.value === src) return
  mcpSource.value = src
  query.value = ''
  // 重置筛选状态（用 silent 跳过 watch 触发的重复搜索，下面会统一 search）
  smitheryCategorySilent = true
  smitheryFilter.value = { category: ALL_VALUE, deployed: false, verified: false, bySmithery: false }
  officialFilter.value = { registry: ALL_VALUE, transport: ALL_VALUE }
  if (mcpSourceAvailable.value) {
    await market.search(src, '', '', PAGE_SIZE)
  }
}

// Smithery toggle 切换：触发新搜索（重新组合 query 发送服务端过滤）
async function onSmitheryToggleChange() {
  if (!mcpSourceAvailable.value) return
  await market.search(mcpSource.value, buildSmitheryQuery(), '', PAGE_SIZE)
}

// Select 通过 v-model 更新 smitheryFilter.category，watch 触发新搜索
// smitheryCategorySilent 标志用于跳过程序化重置（switchMcpSource / onSearch）触发的重复搜索
let smitheryCategorySilent = false
watch(() => smitheryFilter.value.category, () => {
  if (mcpSource.value !== 'smithery') return
  if (smitheryCategorySilent) {
    smitheryCategorySilent = false
    return
  }
  const categoryQuery = smitheryFilter.value.category === ALL_VALUE ? '' : smitheryFilter.value.category
  const cat = SMITHERY_CATEGORIES.find(c => c.query === categoryQuery)
  query.value = cat?.query ?? ''
  if (mcpSourceAvailable.value) {
    market.search(mcpSource.value, buildSmitheryQuery(), '', PAGE_SIZE)
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
          市场
        </h1>
        <p class="mt-1 text-sm text-muted-foreground">
          发现社区的 MCP 服务器与 Skills 并安装到已启用的 Agent。
        </p>
      </div>
    </div>

    <div ref="scrollContainer" class="flex-1 overflow-y-auto">
      <div class="mx-auto max-w-6xl px-8 py-4">
        <Tabs v-model="mode" class="space-y-6">
          <TabsList>
            <TabsTrigger value="servers" :disabled="!officialEnabled && !smitheryEnabled">
              <PhBooks :size="13" class="mr-1.5" />
              MCP 服务器
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
              v-if="!officialEnabled && !smitheryEnabled"
            >
              <EmptyHeader>
                <EmptyTitle>无可用 MCP 来源</EmptyTitle>
                <EmptyDescription>请在设置中启用 official 或 smithery 市场来源。</EmptyDescription>
              </EmptyHeader>
            </Empty>
            <template v-else>
              <!-- MCP 来源切换 -->
              <div class="flex items-center gap-2">
                <button
                  v-if="officialEnabled"
                  :aria-pressed="mcpSource === 'official'"
                  class="rounded-md px-3 py-1.5 text-xs font-medium transition-all duration-200"
                  :class="mcpSource === 'official' ? 'bg-primary text-primary-foreground shadow' : 'bg-muted/60 text-muted-foreground hover:bg-muted'"
                  @click="switchMcpSource('official')"
                >
                  官方
                </button>
                <button
                  v-if="smitheryEnabled"
                  :aria-pressed="mcpSource === 'smithery'"
                  class="rounded-md px-3 py-1.5 text-xs font-medium transition-all duration-200"
                  :class="mcpSource === 'smithery' ? 'bg-primary text-primary-foreground shadow' : 'bg-muted/60 text-muted-foreground hover:bg-muted'"
                  @click="switchMcpSource('smithery')"
                >
                  Smithery
                </button>
              </div>

              <form class="flex items-center gap-2" @submit.prevent="onSearch">
                <div class="relative flex-1">
                  <PhMagnifyingGlass :size="14" class="absolute left-3 top-1/2 -translate-y-1/2 text-muted-foreground" />
                  <Input
                    v-model="query"
                    :placeholder="mcpSource === 'smithery' ? '搜索 Smithery MCP 服务器（googledrive、dropbox...）' : '搜索 MCP 服务器（filesystem、github、redis...）'"
                    class="pl-9"
                    aria-label="搜索 MCP 服务器"
                  />
                </div>
                <Button type="submit" :disabled="market.loadingServers">
                  <PhMagnifyingGlass :size="14" />
                  搜索
                </Button>
              </form>

              <!-- 筛选条：
                   Smithery：分类下拉 + toggle，通过 query 语法发送服务端过滤（与分类可组合）
                   Official：registry/transport 下拉，仅客户端过滤已加载结果（API 不支持服务端筛选） -->
              <div v-if="market.servers.items.length > 0" class="flex flex-wrap items-center gap-2 rounded-md border border-border bg-muted/20 px-3 py-2">
                <div class="flex items-center gap-1.5 text-xs text-muted-foreground">
                  <PhFunnel :size="12" />
                  筛选
                </div>
                <template v-if="mcpSource === 'smithery'">
                  <Select v-model="smitheryFilter.category">
                    <SelectTrigger size="sm" class="w-40 text-xs">
                      <SelectValue placeholder="选择分类" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem v-for="cat in SMITHERY_CATEGORIES" :key="cat.label" :value="cat.query || ALL_VALUE" class="text-xs">
                        {{ cat.label }}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                  <label class="inline-flex cursor-pointer items-center gap-1.5 text-xs">
                    <Checkbox
                      :model-value="smitheryFilter.bySmithery"
                      @update:model-value="(v: boolean | 'indeterminate') => { smitheryFilter.bySmithery = v === true; onSmitheryToggleChange() }"
                    />
                    Smithery 官方
                  </label>
                  <label class="inline-flex cursor-pointer items-center gap-1.5 text-xs">
                    <Checkbox
                      :model-value="smitheryFilter.deployed"
                      @update:model-value="(v: boolean | 'indeterminate') => { smitheryFilter.deployed = v === true; onSmitheryToggleChange() }"
                    />
                    已部署
                  </label>
                  <label class="inline-flex cursor-pointer items-center gap-1.5 text-xs">
                    <Checkbox
                      :model-value="smitheryFilter.verified"
                      @update:model-value="(v: boolean | 'indeterminate') => { smitheryFilter.verified = v === true; onSmitheryToggleChange() }"
                    />
                    已验证
                  </label>
                  <!-- remote 在 API 中等同 is:deployed，不再单独暴露以避免重复 -->
                </template>
                <template v-else>
                  <Select v-model="officialFilter.registry">
                    <SelectTrigger size="sm" class="w-40 text-xs">
                      <SelectValue placeholder="全部注册表" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem :value="ALL_VALUE" class="text-xs">全部注册表</SelectItem>
                      <SelectItem v-for="r in officialRegistries" :key="r" :value="r" class="text-xs">{{ r }}</SelectItem>
                    </SelectContent>
                  </Select>
                  <Select v-model="officialFilter.transport">
                    <SelectTrigger size="sm" class="w-44 text-xs">
                      <SelectValue placeholder="全部 Transport" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem :value="ALL_VALUE" class="text-xs">全部 Transport</SelectItem>
                      <SelectItem value="stdio" class="text-xs">stdio</SelectItem>
                      <SelectItem value="http" class="text-xs">http</SelectItem>
                      <SelectItem value="sse" class="text-xs">sse</SelectItem>
                    </SelectContent>
                  </Select>
                </template>
                <span class="ml-auto text-[10px] text-muted-foreground">
                  <template v-if="mcpSource === 'smithery'">
                    {{ market.servers.total }} 个
                  </template>
                  <template v-else>
                    已加载 {{ filteredServers.length }} / {{ market.servers.items.length }}（仅过滤已加载）
                  </template>
                </span>
              </div>

              <div v-if="market.loadingServers && !hasResults" class="flex items-center justify-center py-12">
                <Spinner />
              </div>

              <Empty
                v-else-if="!hasResults"
              >
                <EmptyHeader>
                  <EmptyTitle>未找到服务器</EmptyTitle>
                  <EmptyDescription>尝试不同的搜索关键词，或调整筛选条件。</EmptyDescription>
                </EmptyHeader>
              </Empty>

              <div v-else class="grid grid-cols-1 gap-4 lg:grid-cols-2">
                <MarketCard
                  v-for="server in filteredServers"
                  :key="server.sourceId"
                  :server="server"
                />
              </div>

              <!-- 无限滚动提示行（与 Skills tab 统一） -->
              <div
                v-if="hasResults && market.servers.hasMore"
                class="flex items-center justify-center gap-2 py-6 text-sm text-muted-foreground"
              >
                <template v-if="market.loadingServers">
                  <Spinner />
                  <span>加载中...</span>
                </template>
                <template v-else>
                  <span>滚动加载更多</span>
                </template>
              </div>
              <div
                v-else-if="hasResults && market.servers.total > 0 && !market.servers.hasMore"
                class="flex items-center justify-center py-6 text-sm text-muted-foreground"
              >
                <span>已显示全部 {{ market.servers.total }} 个服务器</span>
              </div>
            </template>
          </TabsContent>

          <!-- Skills Tab -->
          <TabsContent value="skills" class="space-y-4">
            <Empty
              v-if="!skillsSourceEnabled"
            >
              <EmptyHeader>
                <EmptyTitle>Skills 来源已禁用</EmptyTitle>
                <EmptyDescription>请在设置中启用 github 或 skills-sh 来源。</EmptyDescription>
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
                  GitHub 仓库
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
                    :placeholder="skillSource === 'skills-sh' ? '搜索 skills.sh（至少 2 个字符）' : '搜索 GitHub 仓库 Skills（空查询浏览全部）'"
                    class="pl-9"
                    aria-label="搜索 Skills"
                  />
                </div>
                <Button type="submit" :disabled="market.loadingSkills">
                  <PhMagnifyingGlass :size="14" />
                  搜索
                </Button>
              </form>

              <p v-if="skillQuery.trim().length > 0 && skillQuery.trim().length < 2" class="text-xs text-muted-foreground">
                skills.sh API 要求查询至少 2 个字符。
              </p>

              <div v-if="market.loadingSkills && !hasSkillResults" class="flex items-center justify-center py-12">
                <Spinner />
              </div>

              <Empty
                v-else-if="!skillsSearched"
              >
                <EmptyHeader>
                  <EmptyTitle>{{ skillSource === 'skills-sh' ? '搜索 skills.sh Skills' : '搜索 GitHub Skills' }}</EmptyTitle>
                  <EmptyDescription>{{ skillSource === 'skills-sh'
                    ? '输入关键词搜索 skills.sh（至少 2 个字符）。'
                    : '留空浏览已配置 GitHub 仓库中的全部 Skills，或输入关键词搜索。' }}</EmptyDescription>
                </EmptyHeader>
              </Empty>

              <Empty
                v-else-if="!hasSkillResults"
              >
                <EmptyHeader>
                  <EmptyTitle>{{ skillSource === 'skills-sh' ? '未找到 skills.sh Skills' : '未找到 GitHub Skills' }}</EmptyTitle>
                  <EmptyDescription>{{ skillSource === 'skills-sh'
                    ? '尝试不同的关键词。'
                    : '尝试不同的关键词，或留空浏览全部。可在设置中添加更多 GitHub 仓库。' }}</EmptyDescription>
                </EmptyHeader>
              </Empty>

              <div v-else class="grid grid-cols-1 gap-4 lg:grid-cols-2">
                <SkillMarketCard
                  v-for="skill in market.skills.items"
                  :key="skill.id"
                  :skill="skill"
                />
              </div>

              <!-- 无限滚动哨兵：进入视口时自动加载下一页 -->
              <div
                v-if="hasSkillResults && market.skills.hasMore"
                class="flex items-center justify-center gap-2 py-6 text-sm text-muted-foreground"
              >
                <template v-if="market.loadingSkills">
                  <Spinner />
                  <span>加载中...</span>
                </template>
                <template v-else>
                  <span>滚动加载更多</span>
                </template>
              </div>
              <div
                v-else-if="hasSkillResults && market.skills.total > 0 && !market.skills.hasMore"
                class="flex items-center justify-center py-6 text-sm text-muted-foreground"
              >
                <span>已显示全部 {{ market.skills.total }} 个 skill</span>
              </div>
            </template>
          </TabsContent>
        </Tabs>

        <p v-if="market.error" class="mt-4 text-xs text-destructive">
          {{ market.error }}
        </p>
      </div>
      <!-- 无限滚动哨兵：始终在 DOM 中，确保 IntersectionObserver 可靠触发。
           当任一 tab 有更多数据且正在加载时也保留触发能力。 -->
      <div ref="sentinel" class="h-4" :class="{ 'opacity-0': !currentHasMore && !currentLoading }" />
    </div>
  </div>
</template>
