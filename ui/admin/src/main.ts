import { createPinia } from 'pinia'
import { createApp } from 'vue'
import App from './App.vue'
import { i18n } from './i18n'
import { loadRuntimeConfig } from './lib/runtime-config'
import { makeRouter } from './router'
import { usePreferences } from './stores/preferences'
import { useSession } from './stores/session'
import './style.css'

const pinia = createPinia()
const app = createApp(App).use(pinia).use(i18n)
usePreferences() // applies the theme before the first paint of the app

const { idleSeconds } = await loadRuntimeConfig()
useSession().setIdleSeconds(idleSeconds)

const router = makeRouter()
app.use(router)
await router.isReady()
app.mount('#app')
