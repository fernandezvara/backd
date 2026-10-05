<script setup lang="ts">
import { toRef } from 'vue'
import { provideForm } from '@/lib/form-context'
import type { Schema } from '@/lib/schema'
import SchemaField from './SchemaField.vue'

// A document as a form, drawn from the collection's schema. The model is the
// document's own fields (never `id` or `_meta`).
const props = defineProps<{ schema: Schema; readonly?: boolean; errors?: Record<string, string> }>()
const model = defineModel<Record<string, unknown>>({ required: true })

provideForm({ root: props.schema, readonly: toRef(() => !!props.readonly), errors: toRef(() => props.errors ?? {}) })
</script>

<template>
  <SchemaField v-model="model" :schema="schema" path="" label="" bare />
</template>
