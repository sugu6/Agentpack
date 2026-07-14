import { createRouter, createWebHashHistory } from 'vue-router'
import { i18n } from '@/i18n'

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    {
      path: '/',
      name: 'agents',
      component: () => import('@/views/AgentsView.vue'),
      meta: { title: () => i18n.global.t('nav.agents') },
    },
    {
      path: '/mcp',
      name: 'mcp',
      component: () => import('@/views/McpView.vue'),
      meta: { title: () => i18n.global.t('nav.mcp') },
    },
    {
      path: '/skills',
      name: 'skills',
      component: () => import('@/views/SkillsView.vue'),
      meta: { title: () => i18n.global.t('nav.skills') },
    },
    {
      path: '/market',
      name: 'market',
      component: () => import('@/views/MarketView.vue'),
      meta: { title: () => i18n.global.t('nav.market') },
    },
    {
      path: '/settings',
      name: 'settings',
      component: () => import('@/views/SettingsView.vue'),
      meta: { title: () => i18n.global.t('nav.settings') },
    },
    {
      path: '/:pathMatch(.*)*',
      redirect: '/',
    },
  ],
})

router.afterEach((to) => {
  const title = to.meta.title
  const resolved = typeof title === 'function' ? (title as () => string)() : title
  if (resolved) {
    document.title = `${resolved} · AgentPack`
  }
})

export default router
