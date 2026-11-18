import { expect, test } from 'vitest'
import { tabIcon } from './tabIcons'

// The tabs of the pages and the sections that are pages: each one has its icon, so a new tab can't ship without.
const TABS = [
  'records', 'schema', 'rules', 'triggers', 'checks', 'api', // collection
  'form', 'json', 'files', 'history', // document
  'code', 'invocations', 'settings', 'usedby', // function
  'overview', 'security', 'sessions', 'privacy', 'members', 'sso', 'domains', 'teams', 'invitations', // user, organization
  'secrets', 'keys', 'encrypted', 'migrations', // secrets, data checks
]
const SECTIONS = ['config', 'config-settings', 'config-signin', 'config-templates', 'config-files', 'config-backup', 'config-api', 'functions', 'functions-triggers', 'functions-history', 'functions-jobs']

test('every tab and every section that is a page has an icon', () => {
  for (const id of [...TABS, ...SECTIONS]) expect(tabIcon(id), id).toBeDefined()
})

test('a tab nobody listed has none', () => {
  expect(tabIcon('nope')).toBeUndefined()
})
