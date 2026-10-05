import { inject, provide, type InjectionKey, type Ref } from 'vue'
import type { Schema } from './schema'

export interface FormContext {
  /** The collection's whole schema: `$ref`s resolve against it. */
  root: Schema
  readonly: Ref<boolean>
  /** Server validation messages by dot path. */
  errors: Ref<Record<string, string>>
}

const KEY: InjectionKey<FormContext> = Symbol('schema-form')

export const provideForm = (ctx: FormContext) => provide(KEY, ctx)

export function useForm(): FormContext {
  const ctx = inject(KEY)
  if (!ctx) throw new Error('SchemaField needs a SchemaForm around it')
  return ctx
}
