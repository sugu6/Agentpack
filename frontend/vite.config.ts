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
      onLog(level, log) {
        // 抑制 @vueuse/core 的 __PURE__ 注释位置警告（第三方库问题，不影响构建）
        if (log.message?.includes('__PURE__')) return
        // 抑制 i18n 动态导入不拆 chunk 的提示（已改为静态导入，但保留防御）
        if (log.message?.includes('will not move module into another chunk')) return
      },
    },
  },
})
