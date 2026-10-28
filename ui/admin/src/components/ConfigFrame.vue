<script setup lang="ts">
import { onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRealmConfig } from '@/stores/config'
import AppAlert from './AppAlert.vue'
import AppButton from './AppButton.vue'
import PageHeader from './PageHeader.vue'
import ConfigTabs from './ConfigTabs.vue'

// The frame of every configuration page: title, tabs, and the loading and
// failure states; the slot shows once the configuration is there.
defineProps<{ heading?: string }>()
const { t } = useI18n()
const cfg = useRealmConfig()
onMounted(() => cfg.load())
</script>

<template>
  <PageHeader :title="t('config.title')" :description="t('config.help')">
    <AppButton variant="secondary" :busy="cfg.busy" @click="cfg.load(true)">{{ t('common.reload') }}</AppButton>
  </PageHeader>
  <ConfigTabs />

  <AppAlert v-if="cfg.error" kind="error">{{ cfg.error || t('config.loadFailed') }}</AppAlert>
  <p v-else-if="!cfg.config" class="text-sm" role="status">{{ t('config.loading') }}</p>
  <template v-else>
    <h2 v-if="heading" class="sr-only">{{ heading }}</h2>
    <slot :config="cfg.config" />
  </template>
</template>
