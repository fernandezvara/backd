<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

// A JSON editor for what a form can't draw: a field, or a whole document.
// The text is the editor's own while it is being typed; only valid JSON
// reaches the model.
const props = defineProps<{ label: string; id: string; error?: string; readonly?: boolean; rows?: number; describedBy?: string; hint?: string }>()
const model = defineModel<unknown>()
const { t } = useI18n()

const pretty = (v: unknown) => (v === undefined ? '' : JSON.stringify(v, null, 2))
const text = ref(pretty(model.value))
const parseError = ref('')
let last = model.value

// A change from outside (reloading a newer version) replaces the text.
watch(model, (v) => {
  if (JSON.stringify(v) !== JSON.stringify(last)) {
    text.value = pretty(v)
    parseError.value = ''
    last = v
  }
})

function input(e: Event) {
  text.value = (e.target as HTMLTextAreaElement).value
  if (text.value.trim() === '') {
    parseError.value = ''
    last = undefined
    model.value = undefined
    return
  }
  try {
    const v = JSON.parse(text.value)
    parseError.value = ''
    last = v
    model.value = v
  } catch {
    parseError.value = t('data.form.invalidJson')
  }
}
const shown = computed(() => props.error || parseError.value)
</script>

<template>
  <div class="space-y-1">
    <label :for="id" class="block text-sm font-medium">{{ label }} <span class="font-normal text-slate-600 dark:text-slate-400">(JSON)</span></label>
    <textarea
      :id="id"
      :value="text"
      :rows="rows ?? 6"
      :readonly="readonly"
      spellcheck="false"
      :aria-invalid="shown ? true : undefined"
      :aria-describedby="shown ? `${id}-error` : describedBy"
      class="block w-full rounded-md border border-slate-300 bg-white px-3 py-2 font-mono text-xs dark:border-slate-600 dark:bg-slate-900"
      @input="input"
    />
    <p v-if="hint" :id="describedBy" class="text-xs text-slate-600 dark:text-slate-400">{{ hint }}</p>
    <p v-if="shown" :id="`${id}-error`" class="text-xs text-red-700 dark:text-red-400">{{ shown }}</p>
  </div>
</template>
