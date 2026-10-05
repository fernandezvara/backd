import { defineStore } from 'pinia'
import { ref, watch } from 'vue'

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

export const usePreferences = defineStore('preferences', () => {
  const theme = ref<Theme>(read())
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
  return { theme }
})
