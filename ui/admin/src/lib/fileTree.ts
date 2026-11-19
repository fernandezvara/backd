// A flat list of paths as a folder tree, for the pages that show a realm's files.
export interface TreeNode {
  name: string
  path: string
  dir: boolean
  children: TreeNode[]
  /** Files below (a file counts itself). */
  count: number
}

export function buildTree(paths: string[]): TreeNode {
  const root: TreeNode = { name: '', path: '', dir: true, children: [], count: 0 }
  for (const full of paths) {
    const parts = full.split('/')
    let node = root
    parts.forEach((part, i) => {
      const path = parts.slice(0, i + 1).join('/')
      let next = node.children.find((c) => c.name === part)
      if (!next) {
        next = { name: part, path, dir: i < parts.length - 1, children: [], count: 0 }
        node.children.push(next)
      }
      node = next
    })
  }
  const finish = (n: TreeNode): number => {
    // Folders first, then files, each by name.
    n.children.sort((a, b) => Number(b.dir) - Number(a.dir) || a.name.localeCompare(b.name))
    n.count = n.dir ? n.children.reduce((sum, c) => sum + finish(c), 0) : 1
    return n.count
  }
  finish(root)
  return root
}

export interface TreeRow {
  node: TreeNode
  depth: number
}

/** The rows to draw: every node whose folders are all open, in order. */
export function visibleRows(root: TreeNode, open: Set<string>): TreeRow[] {
  const rows: TreeRow[] = []
  const walk = (n: TreeNode, depth: number) => {
    for (const c of n.children) {
      rows.push({ node: c, depth })
      if (c.dir && open.has(c.path)) walk(c, depth + 1)
    }
  }
  walk(root, 0)
  return rows
}
