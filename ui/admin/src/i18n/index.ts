import { createI18n } from 'vue-i18n'
import en from './en.json'
import es from './es.json'
import { detectLocale } from './locale'

// Every string of the interface lives in the message files: English and
// Spanish, with English as the fallback. The preferences store sets the
// locale from the administrator's choice; until then, from the browser.
export const i18n = createI18n({
  legacy: false,
  locale: detectLocale(typeof navigator === 'undefined' ? [] : navigator.languages),
  fallbackLocale: 'en',
  messages: { en, es },
  missingWarn: true,
})

export const t = i18n.global.t
