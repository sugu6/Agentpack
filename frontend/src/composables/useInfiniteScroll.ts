import { onBeforeUnmount, onMounted, ref, watch, type Ref } from 'vue'

export interface UseInfiniteScrollOptions {
  /** 滚动容器,作为 IntersectionObserver root。null 表示浏览器视口 */
  scrollRoot: Ref<HTMLElement | null>
  /** 是否还有更多数据(为 false 时回调不会触发,observer 仍保持连接以便状态切换后立即响应) */
  hasMore: Ref<boolean>
  /** 是否正在加载(加载中不重复触发) */
  loading: Ref<boolean>
  /** 触发加载更多 */
  onLoadMore: () => void | Promise<void>
  /** 提前预加载距离,默认 300px */
  rootMargin?: string
}

/**
 * 为单个列表提供独立的 IntersectionObserver 无限滚动监听。
 *
 * 每个 ListMore 实例都拥有自己的 sentinel 和 observer,
 * 避免多个列表共用一个 sentinel 时因 tab 切换导致误触发或漏触发。
 *
 * - 自动在 onMounted 时连接、onBeforeUnmount 时断开
 * - sentinel 引用变化时(v-if 切换)自动重连
 * - scrollRoot 变化时自动重连
 * - 回调内部会再次校验 hasMore/loading,避免竞态
 */
export function useInfiniteScroll(options: UseInfiniteScrollOptions) {
  const sentinel = ref<HTMLElement | null>(null)
  let observer: IntersectionObserver | null = null

  function disconnect() {
    observer?.disconnect()
    observer = null
  }

  function connect() {
    disconnect()
    if (!sentinel.value) return
    observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (!entry.isIntersecting) continue
          // 双重校验,避免与状态切换的竞态
          if (!options.hasMore.value) continue
          if (options.loading.value) continue
          void options.onLoadMore()
        }
      },
      {
        root: options.scrollRoot.value,
        rootMargin: options.rootMargin ?? '0px 0px 300px 0px',
      },
    )
    observer.observe(sentinel.value)
  }

  onMounted(() => {
    connect()
  })

  // sentinel 元素变化(v-if/v-show 切换、key 变化)时自动重连
  watch(sentinel, (el) => {
    if (el) connect()
    else disconnect()
  })

  // 滚动容器变化时重连
  watch(options.scrollRoot, () => {
    if (sentinel.value) connect()
  })

  // loading 结束或 hasMore 变为 true 时重连,让 observer 重新评估可见性
  // 解决 sentinel 一直可见时 observer 不重复触发的问题:
  //   首次加载完成 → sentinel 仍在可视区(可见性未变)→ observer 不触发 → 不继续加载
  //   重新连接后新 observer 会立即评估,若仍可见则继续触发
  watch([options.loading, options.hasMore], ([newLoading, newHasMore], [oldLoading, oldHasMore]) => {
    const loadingFinished = oldLoading && !newLoading
    const hasMoreTurnedTrue = !oldHasMore && newHasMore
    if ((loadingFinished || hasMoreTurnedTrue) && sentinel.value) {
      connect()
    }
  })

  onBeforeUnmount(() => {
    disconnect()
  })

  return { sentinel }
}
