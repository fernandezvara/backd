import {
  PhArchive, PhArrowsLeftRight, PhBracketsCurly, PhClockCounterClockwise, PhCode, PhDevices, PhEnvelopeSimple, PhFolders, PhFunction, PhGear, PhGitFork, PhGlobe, PhKey, PhLightning,
  PhListBullets, PhListChecks, PhLock, PhLockKey, PhLockSimple, PhPaperclip, PhPlayCircle, PhRows, PhSealCheck, PhShieldCheck, PhSignIn, PhSliders, PhSquaresFour, PhUsers, PhUsersThree,
} from '@phosphor-icons/vue'
import type { Component } from 'vue'

/**
 * The icon a tab shows next to its label, by the tab's id (tabs inside a page) or route name (tabs that
 * are pages). One place, so the same section has the same icon wherever it appears.
 */
const ICONS: Record<string, Component> = {
  // collection and document
  records: PhRows, schema: PhBracketsCurly, rules: PhShieldCheck, triggers: PhLightning, api: PhCode,
  form: PhListBullets, json: PhBracketsCurly, files: PhPaperclip, history: PhClockCounterClockwise,
  // function
  code: PhCode, invocations: PhPlayCircle, settings: PhGear, usedby: PhGitFork,
  // user and organization
  overview: PhSquaresFour, security: PhShieldCheck, sessions: PhDevices, privacy: PhLockKey,
  members: PhUsers, sso: PhKey, domains: PhGlobe, teams: PhUsersThree, invitations: PhEnvelopeSimple,
  // secrets, data checks
  secrets: PhLockSimple, keys: PhKey, encrypted: PhLock, checks: PhSealCheck, migrations: PhArrowsLeftRight,
  // configuration and functions sections (route names)
  config: PhSquaresFour, 'config-settings': PhSliders, 'config-signin': PhSignIn, 'config-templates': PhEnvelopeSimple, 'config-files': PhFolders, 'config-backup': PhArchive, 'config-api': PhCode,
  functions: PhFunction, 'functions-triggers': PhLightning, 'functions-history': PhClockCounterClockwise, 'functions-jobs': PhListChecks,
}

export const tabIcon = (id: string): Component | undefined => ICONS[id]
