<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppShell from '@/components/AppShell.vue'
import ConfigNotLoaded from '@/components/ConfigNotLoaded.vue'
import ToastHost from '@/components/ToastHost.vue'
import { configNotLoaded } from '@/lib/config-loaded'
import { useSession } from '@/stores/session'

const route = useRoute()
const router = useRouter()
const session = useSession()

// A session that ends while a protected page is open (idle timeout, the
// server refusing the token) goes back to the sign-in page of its realm.
watch(
  () => session.signedIn,
  (signedIn) => {
    if (!signedIn && route.meta.auth && typeof route.params.realm === 'string') {
      void router.replace({ name: 'signin', params: { realm: route.params.realm } })
    }
  },
)

// A dev-mode server whose config hasn't loaded answers nothing but config_not_loaded: say so
// instead of the pages, and look again every few seconds until it has.
const notLoaded = ref<string | null>(null)
let timer: ReturnType<typeof setTimeout> | undefined
async function check() {
  notLoaded.value = await configNotLoaded()
  if (notLoaded.value !== null) timer = setTimeout(check, 3000)
}
onMounted(check)
onUnmounted(() => clearTimeout(timer))
</script>

<template>
  <ConfigNotLoaded v-if="notLoaded !== null" :message="notLoaded" />
  <AppShell v-else-if="session.signedIn && route.meta.auth">
    <!-- Keyed by address: a document that was just created (new → its own page) loads afresh. -->
    <RouterView v-slot="{ Component, route: current }">
      <component :is="Component" :key="current.fullPath" />
    </RouterView>
  </AppShell>
  <RouterView v-else />
  <ToastHost />
</template>
