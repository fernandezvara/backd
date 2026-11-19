import { defineStore } from 'pinia'
import { ref, watch } from 'vue'
import { i18n } from '@/i18n'
import { isLocale, resolveLocale, type LanguagePreference } from '@/i18n/locale'

export type Theme = 'system' | 'light' | 'dark'

/** The accent palettes (`html[data-accent]` in style.css). The first is the default. */
export const ACCENTS = ['teal', 'blue', 'indigo', 'violet', 'rose', 'orange', 'emerald'] as const
export type Accent = (typeof ACCENTS)[number]
const ACCENT_KEY = 'backd-admin:accent'

function readAccent(): Accent {
  try {
    const v = localStorage.getItem(ACCENT_KEY)
    if ((ACCENTS as readonly string[]).includes(v ?? '')) return v as Accent
  } catch {
    // storage blocked: the default
  }
  return 'teal'
}

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
  const accent = ref<Accent>(readAccent())
  watch(
    accent,
    (a) => {
      document.documentElement.dataset.accent = a
      try {
        localStorage.setItem(ACCENT_KEY, a)
      } catch {
        // not persisted; still applied
      }
    },
    { immediate: true },
  )
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
  return { theme, language, accent }
})
