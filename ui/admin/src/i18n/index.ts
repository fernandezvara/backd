import { createI18n } from 'vue-i18n'
import en from './en.json'

// Every string of the interface lives in the message files. English first;
// dates and numbers follow the browser's locale (Intl, not messages).
export const i18n = createI18n({
  legacy: false,
  locale: 'en',
  fallbackLocale: 'en',
  messages: { en },
  missingWarn: true,
})

export const t = i18n.global.t
