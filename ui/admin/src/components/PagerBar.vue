<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import AppButton from './AppButton.vue'

const props = defineProps<{ skip: number; count: number; hasMore: boolean; loading?: boolean }>()
defineEmits<{ page: [delta: -1 | 1] }>()
const { t } = useI18n()
const range = computed(() => ({ from: props.count ? props.skip + 1 : 0, to: props.skip + props.count }))
</script>

<template>
  <div class="mt-3 flex items-center gap-3">
    <AppButton variant="secondary" :disabled="skip === 0 || loading" @click="$emit('page', -1)">{{ t('common.previous') }}</AppButton>
    <AppButton variant="secondary" :disabled="!hasMore || loading" @click="$emit('page', 1)">{{ t('common.next') }}</AppButton>
    <span v-if="count" class="text-sm text-slate-600 dark:text-slate-400">{{ t('pager.showing', range) }}</span>
  </div>
</template>
