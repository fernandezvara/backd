<script setup lang="ts">
// One field of a form drawn from a JSON Schema, and (for objects and arrays)
// the fields inside it. Kinds a form can't draw become a JSON editor.
import { computed, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import { useForm } from '@/lib/form-context'
import { initialValue, isoToLocalInput, join, localInputToIso, resolve, type Schema } from '@/lib/schema'
import AppButton from './AppButton.vue'
import JsonField from './JsonField.vue'

const props = defineProps<{ schema: Schema; path: string; label: string; required?: boolean; refs?: string[]; bare?: boolean }>()
const model = defineModel<unknown>()
const { t } = useI18n()
const form = useForm()
const id = useId()

const refs = computed(() => props.refs ?? [])
const r = computed(() => resolve(props.schema, form.root, refs.value))
const s = computed(() => r.value.schema)
// A $ref taken here counts for everything inside, so a cycle is cut where it closes.
const childRefs = computed(() => (typeof props.schema.$ref === 'string' ? [...refs.value, props.schema.$ref as string] : refs.value))
const error = computed(() => form.errors.value[props.path])
const readonly = computed(() => form.readonly.value)
const describedBy = computed(() => (error.value ? `${id}-error` : s.value.description ? `${id}-help` : undefined))

const properties = computed(() => Object.entries((s.value.properties ?? {}) as Record<string, Schema>))
const requiredKeys = computed<string[]>(() => (Array.isArray(s.value.required) ? s.value.required : []))
const asObject = computed(() => (model.value !== null && typeof model.value === 'object' && !Array.isArray(model.value) ? (model.value as Record<string, unknown>) : {}))
const asArray = computed(() => (Array.isArray(model.value) ? (model.value as unknown[]) : []))
const present = computed(() => model.value !== undefined && model.value !== null)

function setKey(key: string, value: unknown) {
  const next = { ...asObject.value }
  if (value === undefined || (value === '' && !requiredKeys.value.includes(key))) delete next[key]
  else next[key] = value
  model.value = next
}
function setItem(i: number, value: unknown) {
  const next = [...asArray.value]
  next[i] = value
  model.value = next
}
function addItem() {
  model.value = [...asArray.value, initialValue(s.value.items as Schema, form.root, childRefs.value) ?? (resolve(s.value.items as Schema, form.root, childRefs.value).kind === 'object' ? {} : '')]
}
function removeItem(i: number) {
  model.value = asArray.value.filter((_, j) => j !== i)
}
function move(i: number, delta: -1 | 1) {
  const next = [...asArray.value]
  const j = i + delta
  if (j < 0 || j >= next.length) return
  ;[next[i], next[j]] = [next[j], next[i]]
  model.value = next
}
const addOptional = () => (model.value = initialValue(s.value, form.root, refs.value) ?? (r.value.kind === 'array' ? [] : {}))

// Scalars
const inputType = computed(() => {
  switch (s.value.format) {
    case 'email':
      return 'email'
    case 'uri':
      return 'url'
    case 'date':
      return 'date'
    case 'date-time':
      return 'datetime-local'
    default:
      return 'text'
  }
})
const multiline = computed(() => !s.value.format && (typeof model.value === 'string' && model.value.includes('\n') || !s.value.maxLength || s.value.maxLength > 120))
const stringValue = computed(() => {
  const v = model.value
  if (typeof v !== 'string') return ''
  return s.value.format === 'date-time' ? isoToLocalInput(v) : v
})
function onString(e: Event) {
  const v = (e.target as HTMLInputElement).value
  model.value = s.value.format === 'date-time' ? (v === '' ? '' : localInputToIso(v)) : v
}
function onNumber(e: Event) {
  const raw = (e.target as HTMLInputElement).value
  model.value = raw === '' ? undefined : Number(raw)
}
const step = computed(() => (r.value.kind === 'integer' ? 1 : s.value.multipleOf ?? 'any'))

const options = computed<unknown[]>(() => (r.value.kind === 'enum' ? (s.value.enum as unknown[]) : []))
const optionIndex = computed(() => options.value.findIndex((o) => o === model.value))
function onEnum(e: Event) {
  const i = (e.target as HTMLSelectElement).value
  model.value = i === '' ? undefined : options.value[Number(i)]
}
function onBool(e: Event) {
  const v = (e.target as HTMLSelectElement).value
  model.value = v === '' ? undefined : v === 'true'
}

const limits = computed(() => {
  const out: string[] = []
  if (s.value.minLength !== undefined) out.push(t('data.form.minLength', { n: s.value.minLength }))
  if (s.value.maxLength !== undefined) out.push(t('data.form.maxLength', { n: s.value.maxLength }))
  if (s.value.minimum !== undefined) out.push(t('data.form.minimum', { n: s.value.minimum }))
  if (s.value.maximum !== undefined) out.push(t('data.form.maximum', { n: s.value.maximum }))
  if (s.value.exclusiveMinimum !== undefined) out.push(t('data.form.exclusiveMinimum', { n: s.value.exclusiveMinimum }))
  if (s.value.exclusiveMaximum !== undefined) out.push(t('data.form.exclusiveMaximum', { n: s.value.exclusiveMaximum }))
  if (s.value.pattern) out.push(t('data.form.pattern', { p: s.value.pattern }))
  return out
})
const inputClass = 'block w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm dark:border-slate-600 dark:bg-slate-900'
</script>

<template>
  <!-- objects: a fieldset of their properties -->
  <component :is="bare ? 'div' : 'fieldset'" v-if="r.kind === 'object'" :class="bare ? 'space-y-4' : 'space-y-4 rounded-md border border-slate-200 p-3 dark:border-slate-700'" :data-path="path">
    <legend v-if="!bare" class="px-1 text-sm font-medium">
      {{ label }}<span v-if="required" aria-hidden="true" class="text-red-700"> *</span>
      <span v-if="required" class="sr-only"> ({{ t('data.form.required') }})</span>
    </legend>
    <p v-if="!bare && s.description" :id="`${id}-help`" class="text-xs text-slate-600 dark:text-slate-400">{{ s.description }}</p>
    <template v-if="present || required || bare">
      <SchemaField
        v-for="[key, prop] in properties"
        :key="key"
        :model-value="asObject[key]"
        :schema="prop"
        :path="join(path, key)"
        :label="(prop.title as string) || key"
        :required="requiredKeys.includes(key)"
        :refs="childRefs"
        @update:model-value="(v: unknown) => setKey(key, v)"
      />
      <AppButton v-if="!bare && !required && !readonly" variant="secondary" @click="model = undefined">{{ t('data.form.remove') }}</AppButton>
    </template>
    <AppButton v-else-if="!readonly" variant="secondary" @click="addOptional">{{ t('data.form.add', { name: label }) }}</AppButton>
    <p v-if="error" :id="`${id}-error`" class="text-xs text-red-700 dark:text-red-400">{{ error }}</p>
  </component>

  <!-- arrays: items to add, remove and reorder -->
  <fieldset v-else-if="r.kind === 'array'" class="space-y-3 rounded-md border border-slate-200 p-3 dark:border-slate-700" :data-path="path">
    <legend class="px-1 text-sm font-medium">
      {{ label }}<span v-if="required" aria-hidden="true" class="text-red-700"> *</span>
      <span v-if="required" class="sr-only"> ({{ t('data.form.required') }})</span>
    </legend>
    <p v-if="s.description" :id="`${id}-help`" class="text-xs text-slate-600 dark:text-slate-400">{{ s.description }}</p>
    <ol v-if="asArray.length" class="space-y-3">
      <li v-for="(item, i) in asArray" :key="i" class="flex items-start gap-2">
        <div class="flex-1">
          <SchemaField :model-value="item" :schema="s.items as Schema" :path="join(path, i)" :label="`${label} ${i + 1}`" :required="true" :refs="childRefs" @update:model-value="(v: unknown) => setItem(i, v)" />
        </div>
        <div v-if="!readonly" class="flex flex-col gap-1 text-xs">
          <button type="button" class="underline" :disabled="i === 0" :aria-label="t('data.form.moveUp', { name: `${label} ${i + 1}` })" @click="move(i, -1)">↑</button>
          <button type="button" class="underline" :disabled="i === asArray.length - 1" :aria-label="t('data.form.moveDown', { name: `${label} ${i + 1}` })" @click="move(i, 1)">↓</button>
          <button type="button" class="underline" :aria-label="t('data.form.removeItem', { name: `${label} ${i + 1}` })" @click="removeItem(i)">✕</button>
        </div>
      </li>
    </ol>
    <p v-else class="text-sm text-slate-600 dark:text-slate-400">{{ t('data.form.noItems') }}</p>
    <AppButton v-if="!readonly" variant="secondary" :disabled="s.maxItems !== undefined && asArray.length >= s.maxItems" @click="addItem">{{ t('data.form.addItem', { name: label }) }}</AppButton>
    <p v-if="error" :id="`${id}-error`" class="text-xs text-red-700 dark:text-red-400">{{ error }}</p>
  </fieldset>

  <!-- what a form can't draw -->
  <div v-else-if="r.kind === 'json'" :data-path="path">
    <JsonField :id="id" v-model="model" :label="label" :error="error" :readonly="readonly" :hint="t('data.form.jsonFallback', { reason: r.reason ?? '' })" :described-by="`${id}-help`" />
  </div>

  <!-- scalars -->
  <div v-else class="space-y-1" :data-path="path">
    <label :for="id" class="block text-sm font-medium">
      {{ label }}<span v-if="required" aria-hidden="true" class="text-red-700"> *</span>
      <span v-if="required" class="sr-only"> ({{ t('data.form.required') }})</span>
    </label>

    <select v-if="r.kind === 'enum'" :id="id" :disabled="readonly" :aria-invalid="error ? true : undefined" :aria-describedby="describedBy" :class="inputClass" :value="optionIndex < 0 ? '' : String(optionIndex)" @change="onEnum">
      <option v-if="!required || optionIndex < 0" value="">—</option>
      <option v-for="(o, i) in options" :key="i" :value="String(i)">{{ String(o) }}</option>
    </select>

    <select v-else-if="r.kind === 'boolean'" :id="id" :disabled="readonly" :aria-invalid="error ? true : undefined" :aria-describedby="describedBy" :class="inputClass" :value="typeof model === 'boolean' ? String(model) : ''" @change="onBool">
      <option v-if="!required || typeof model !== 'boolean'" value="">—</option>
      <option value="true">{{ t('common.yes') }}</option>
      <option value="false">{{ t('common.no') }}</option>
    </select>

    <input
      v-else-if="r.kind === 'number' || r.kind === 'integer'"
      :id="id"
      type="number"
      :step="step"
      :min="s.minimum"
      :max="s.maximum"
      :value="typeof model === 'number' ? model : ''"
      :readonly="readonly"
      :aria-invalid="error ? true : undefined"
      :aria-describedby="describedBy"
      :class="inputClass"
      @input="onNumber"
    />

    <textarea
      v-else-if="multiline"
      :id="id"
      rows="3"
      :value="stringValue"
      :minlength="s.minLength"
      :maxlength="s.maxLength"
      :readonly="readonly"
      :aria-invalid="error ? true : undefined"
      :aria-describedby="describedBy"
      :class="inputClass"
      @input="onString"
    />
    <input
      v-else
      :id="id"
      :type="inputType"
      :value="stringValue"
      :minlength="s.minLength"
      :maxlength="s.maxLength"
      :readonly="readonly"
      :aria-invalid="error ? true : undefined"
      :aria-describedby="describedBy"
      :class="inputClass"
      @input="onString"
    />

    <p v-if="s.description && !error" :id="`${id}-help`" class="text-xs text-slate-600 dark:text-slate-400">{{ s.description }}</p>
    <p v-if="limits.length && !error" class="text-xs text-slate-600 dark:text-slate-400">{{ limits.join(' · ') }}</p>
    <p v-if="error" :id="`${id}-error`" class="text-xs text-red-700 dark:text-red-400">{{ error }}</p>
  </div>
</template>
