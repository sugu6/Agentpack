import {defineConfig} from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import wails from '@wailsio/runtime/plugins/vite'
import path from 'path'

export default defineConfig({
  plugins: [vue(), tailwindcss(), wails('./bindings')],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    host: '127.0.0.1',
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
    allowedHosts: ['wails.localhost'],
  },
  build: {
    rollupOptions: {
      output: {
        // vendor 按职责拆 chunk：单文件更小（主 chunk 曾超 500KB 触发构建
        // 警告），浏览器可并行解析；分组名随依赖稳定，业务代码改动不会
        // 使整个 vendor 失效。marked/dompurify 不在此列——它们仅由
        // UpdateDialog 动态 import，由 Rollup 自动拆为按需加载的异步 chunk。
        manualChunks(id) {
          if (!id.includes('node_modules')) return
          if (id.includes('reka-ui') || id.includes('vue-sonner') || id.includes('@phosphor-icons') || id.includes('@lucide')) return 'ui'
          if (id.includes('vue-i18n') || id.includes('@intlify')) return 'i18n'
          if (id.includes('@vue/') || /[\\/]node_modules[\\/](vue|vue-router|pinia)[\\/]/.test(id)) return 'vue'
          if (id.includes('@wailsio')) return 'wails'
          if (id.includes('tailwind-merge') || id.includes('clsx') || id.includes('class-variance-authority')) return 'utils'
        },
      },
      onLog(level, log) {
        // 抑制 @vueuse/core 的 __PURE__ 注释位置警告（第三方库问题，不影响构建）
        if (log.message?.includes('__PURE__')) return
        // 抑制 i18n 动态导入不拆 chunk 的提示（已改为静态导入，但保留防御）
        if (log.message?.includes('will not move module into another chunk')) return
      },
    },
  },
})
