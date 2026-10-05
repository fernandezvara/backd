// Which language the interface speaks: the one the administrator chose, or
// the first of the browser's languages that has a message file.

export const LOCALES = ['en', 'es'] as const
export type Locale = (typeof LOCALES)[number]
export type LanguagePreference = 'auto' | Locale

/** Each language in its own words: the choices are never translated. */
export const LOCALE_NAMES: Record<Locale, string> = { en: 'English', es: 'Español' }

export function isLocale(value: unknown): value is Locale {
  return typeof value === 'string' && (LOCALES as readonly string[]).includes(value)
}

/** The browser's preferred languages ("es-ES", "en-US", …) matched on their primary subtag; English when none is offered. */
export function detectLocale(languages: readonly string[]): Locale {
  for (const tag of languages) {
    const primary = tag.toLowerCase().split('-')[0]
    if (isLocale(primary)) return primary
  }
  return 'en'
}

export function resolveLocale(preference: LanguagePreference, languages: readonly string[]): Locale {
  return preference === 'auto' ? detectLocale(languages) : preference
}
