<script setup lang="ts">
// The top of a page: its title and what it is for on the left, its actions on the right.
defineProps<{ title: string; description?: string; testid?: string }>()
</script>

<template>
  <div class="mb-5 flex flex-wrap items-start gap-x-4 gap-y-2">
    <div>
      <h1 class="text-xl font-semibold" :data-testid="testid">{{ title }}</h1>
      <p v-if="description" class="max-w-2xl text-sm text-slate-600 dark:text-slate-400">{{ description }}</p>
    </div>
    <div v-if="$slots.default" class="ml-auto flex flex-wrap items-center gap-2 self-center">
      <slot />
    </div>
  </div>
</template>
