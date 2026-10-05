import { defineStore } from 'pinia'
import { ref, watch } from 'vue'
import { i18n } from '@/i18n'
import { isLocale, resolveLocale, type LanguagePreference } from '@/i18n/locale'

export type Theme = 'system' | 'light' | 'dark'

const KEY = 'backd-admin:theme'

function read(): Theme {
  try {
    const v = localStorage.getItem(KEY)
    if (v === 'light' || v === 'dark' || v === 'system') return v
  } catch {
    // storage blocked: the system theme
  }
  return 'system'
}

/** Applies the theme: the `dark` class on <html>, following the system by default. */
export function applyTheme(theme: Theme, root: HTMLElement = document.documentElement, prefersDark = matchMedia('(prefers-color-scheme: dark)').matches) {
  const dark = theme === 'dark' || (theme === 'system' && prefersDark)
  root.classList.toggle('dark', dark)
  root.style.colorScheme = dark ? 'dark' : 'light'
}

const LANGUAGE_KEY = 'backd-admin:language'

function readLanguage(): LanguagePreference {
  try {
    const v = localStorage.getItem(LANGUAGE_KEY)
    if (isLocale(v)) return v
  } catch {
    // storage blocked: follow the browser
  }
  return 'auto'
}

/** Speaks the chosen language (or the browser's) to the page: its messages and its `lang`. */
export function applyLanguage(preference: LanguagePreference, languages: readonly string[] = navigator.languages) {
  const locale = resolveLocale(preference, languages)
  i18n.global.locale.value = locale
  document.documentElement.lang = locale
}

export const usePreferences = defineStore('preferences', () => {
  const theme = ref<Theme>(read())
  const language = ref<LanguagePreference>(readLanguage())
  watch(
    language,
    (l) => {
      applyLanguage(l)
      try {
        if (l === 'auto') localStorage.removeItem(LANGUAGE_KEY)
        else localStorage.setItem(LANGUAGE_KEY, l)
      } catch {
        // not persisted; still applied
      }
    },
    { immediate: true },
  )
  watch(
    theme,
    (t) => {
      applyTheme(t)
      try {
        localStorage.setItem(KEY, t)
      } catch {
        // not persisted; still applied
      }
    },
    { immediate: true },
  )
  matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => applyTheme(theme.value))
  return { theme, language }
})
