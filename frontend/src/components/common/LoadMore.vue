<script setup lang="ts">
import { computed, toRef } from 'vue'
import { useI18n } from 'vue-i18n'
import { Spinner } from '@/components/ui'
import { useInfiniteScroll } from '@/composables/useInfiniteScroll'

const props = defineProps<{
  /** 是否正在加载下一页 */
  loading: boolean
  /** 是否还有更多数据可加载 */
  hasMore: boolean
  /** 已加载条数 */
  loaded: number
  /** 总条数(后端返回的 total,用于显示进度) */
  total: number
  /** 滚动容器,作为 IntersectionObserver root */
  scrollRoot: HTMLElement | null
  /** 提前预加载距离,默认 300px */
  rootMargin?: string
  /** 完成态自定义文案(覆盖默认「已显示全部 N 项」) */
  completeText?: string
  /** 等待态自定义文案(覆盖默认「滚动加载更多」) */
  moreText?: string
}>()

const emit = defineEmits<{
  (e: 'load-more'): void
}>()

const { t } = useI18n()

const { sentinel } = useInfiniteScroll({
  scrollRoot: toRef(props, 'scrollRoot'),
  hasMore: toRef(props, 'hasMore'),
  loading: toRef(props, 'loading'),
  rootMargin: props.rootMargin,
  onLoadMore: () => emit('load-more'),
})

// 是否显示「已加载 X / Y」进度文字
// 仅当 total > 0 且已加载条数大于 0 时显示
const showProgress = computed(() => props.total > 0 && props.loaded > 0)
</script>

<template>
  <!--
    sentinel 同时承担两个职责:
    1. 作为 IntersectionObserver 的观察目标,进入视口触发 load-more
    2. 承载加载状态文案
    注意:当 hasMore=false 且 loading=false 时,sentinel 仍然渲染,
    以保证状态切换(如重新搜索后又有更多)时 observer 仍可立即生效。
  -->
  <div
    ref="sentinel"
    class="flex items-center justify-center gap-2 py-1.5 text-sm text-muted-foreground"
    aria-live="polite"
  >
    <template v-if="loading">
      <Spinner class="size-3.5" />
      <span>{{ t('common.loading') }}</span>
    </template>
    <template v-else-if="hasMore">
      <span>{{ moreText ?? t('common.scrollForMore') }}</span>
    </template>
    <template v-else>
      <span v-if="showProgress">
        {{ completeText ?? t('common.allLoaded', { count: total }) }}
      </span>
    </template>
  </div>
</template>
