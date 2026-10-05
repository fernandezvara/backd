<script setup lang="ts">
import { watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppShell from '@/components/AppShell.vue'
import ToastHost from '@/components/ToastHost.vue'
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
</script>

<template>
  <AppShell v-if="session.signedIn && route.meta.auth">
    <RouterView />
  </AppShell>
  <RouterView v-else />
  <ToastHost />
</template>
