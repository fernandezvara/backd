<script setup lang="ts">
import { useRoute } from 'vue-router'
import AppShell from '@/components/AppShell.vue'
import { useSession } from '@/stores/session'

const route = useRoute()
const session = useSession()
</script>

<template>
  <AppShell v-if="session.signedIn && route.meta.auth">
    <RouterView />
  </AppShell>
  <RouterView v-else />
</template>
