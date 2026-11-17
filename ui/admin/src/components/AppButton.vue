<script setup lang="ts">
withDefaults(defineProps<{ variant?: 'primary' | 'secondary' | 'danger'; type?: 'button' | 'submit'; busy?: boolean }>(), {
  variant: 'primary',
  type: 'button',
  busy: false,
})
</script>

<template>
  <button
    :type="type"
    :disabled="busy"
    :aria-busy="busy || undefined"
    class="inline-flex items-center justify-center h-8 gap-1.5 rounded-md px-3 text-[13px] font-medium disabled:cursor-not-allowed disabled:opacity-60"
    :class="{
      'bg-brand-700 text-white hover:bg-brand-800': variant === 'primary',
      'border border-slate-300 bg-white text-slate-800 hover:bg-slate-100 dark:border-slate-600 dark:bg-slate-800 dark:text-slate-100 dark:hover:bg-slate-700': variant === 'secondary',
      'bg-red-700 text-white hover:bg-red-800': variant === 'danger',
    }"
  >
    <slot />
  </button>
</template>
