import { createApp } from 'vue'
import { createPinia } from 'pinia'
import 'vue-sonner/style.css'
import App from './App.vue'
import router from './router'
import { i18n } from './i18n'
import { installLogBridge } from './lib/logBridge'
import './style.css'

// 前端错误进入统一日志（后端 ~/.agentpack/logs），供诊断包导出
installLogBridge()

const app = createApp(App)
app.use(createPinia())
app.use(router)
app.use(i18n)
app.mount('#app')
