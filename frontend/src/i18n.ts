import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'
import zh from './locales/zh.json'
import zhTW from './locales/zh-TW.json'
import en from './locales/en.json'
import credentialZh from './locales/credential-ops.zh.json'
import credentialZhTW from './locales/credential-ops.zh-TW.json'
import credentialEn from './locales/credential-ops.en.json'

const LANG_KEY = 'lang'

function getInitialLang(): string {
  const stored = localStorage.getItem(LANG_KEY)
  if (stored === 'zh' || stored === 'zh-TW' || stored === 'en') return stored
  const browserLanguage = navigator.language.toLowerCase()
  if (browserLanguage.startsWith('zh-hant') || browserLanguage.startsWith('zh-tw') || browserLanguage.startsWith('zh-hk') || browserLanguage.startsWith('zh-mo')) return 'zh-TW'
  return browserLanguage.startsWith('zh') ? 'zh' : 'en'
}

i18n.use(initReactI18next).init({
  resources: {
    zh: { translation: { ...zh, credentialOps: credentialZh } },
    'zh-TW': { translation: { ...zhTW, credentialOps: credentialZhTW } },
    en: { translation: { ...en, credentialOps: credentialEn } },
  },
  lng: getInitialLang(),
  fallbackLng: {
    'zh-TW': ['zh'],
    default: ['zh'],
  },
  interpolation: {
    escapeValue: false,
  },
})

export default i18n
