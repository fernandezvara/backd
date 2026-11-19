import { expect, test } from 'vitest'
import { buildTree, visibleRows } from './fileTree'

const paths = ['realm.yaml', 'main/orders/schema.json', 'main/orders/rules.json', 'main/notes/schema.json', '_functions/greet/index.ts']

test('paths become folders and files, folders first, each by name, with the count of files below', () => {
  const root = buildTree(paths)
  expect(root.children.map((c) => `${c.name}${c.dir ? '/' : ''}`)).toEqual(['_functions/', 'main/', 'realm.yaml'])
  const main = root.children[1]
  expect(main.count).toBe(3)
  expect(main.children.map((c) => c.name)).toEqual(['notes', 'orders'])
  expect(main.children[1].children.map((c) => c.name)).toEqual(['rules.json', 'schema.json'])
  expect(main.children[1].children[0]).toMatchObject({ path: 'main/orders/rules.json', dir: false, count: 1 })
  expect(root.count).toBe(5)
})

test('only the files of open folders are drawn, deeper ones indented', () => {
  const root = buildTree(paths)
  expect(visibleRows(root, new Set()).map((r) => r.node.path)).toEqual(['_functions', 'main', 'realm.yaml'])
  const rows = visibleRows(root, new Set(['main', 'main/orders']))
  expect(rows.map((r) => [r.node.path, r.depth])).toEqual([
    ['_functions', 0], ['main', 0], ['main/notes', 1], ['main/orders', 1], ['main/orders/rules.json', 2], ['main/orders/schema.json', 2], ['realm.yaml', 0],
  ])
})

test('no files, no rows', () => {
  expect(visibleRows(buildTree([]), new Set())).toEqual([])
})
