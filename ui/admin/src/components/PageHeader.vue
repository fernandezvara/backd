<script setup lang="ts">
// The top of a page: what it is for on the left, its actions on the right. The title is for
// assistive technology (and the headings of the tests): the breadcrumb says where you are.
defineProps<{ title: string; description?: string }>()
</script>

<template>
  <div class="mb-4 flex flex-wrap items-center gap-x-4 gap-y-2">
    <h1 class="sr-only">{{ title }}</h1>
    <p v-if="description" class="max-w-2xl text-sm text-slate-600 dark:text-slate-400">{{ description }}</p>
    <div v-if="$slots.default" class="ml-auto flex flex-wrap items-center gap-2">
      <slot />
    </div>
  </div>
</template>
