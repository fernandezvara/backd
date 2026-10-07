import { ConflictError, createClient } from 'backd-js'
import { PASSWORD, REALM, users } from './users'

// The realm lists the admin users by email and gives them their roles when
// they sign up, so signing up is the whole provisioning (a rerun finds
// them there already).
export default async function globalSetup() {
  const url = process.env.E2E_URL ?? 'http://localhost:8080'
  for (const email of Object.values(users)) {
    const client = createClient({ url, realm: REALM })
    try {
      await client.auth.signup({ email, password: PASSWORD })
    } catch (e) {
      if (!(e instanceof ConflictError)) throw e
    }
  }
  await seedProducts(url)
  await setStorageKeys(url)
}

// The data browser's tests need documents to browse and page through: a few
// named ones and enough bulk ones for three pages. Seeded once (a rerun finds
// them there).
async function seedProducts(url: string) {
  const client = createClient({ url, realm: REALM })
  await client.auth.login({ email: users.admin, password: PASSWORD })
  const products = client.admin.data('main', 'products')
  if ((await products.list({ where: { name: { $startsWith: 'Seed ' } }, limit: 1 })).items.length) return
  const named = [
    { name: 'Seed Widget', sku: 'WID-001', price: 9.5, stock: 10, kind: 'tool', published: true, tags: ['sale', 'new'] },
    { name: 'Seed Gadget', sku: 'GAD-002', price: 25, stock: 0, kind: 'toy', published: false },
    { name: 'Seed Novel', sku: 'NOV-003', price: 12, stock: 4, kind: 'book', published: true, released: '2026-01-15T10:00:00.000Z' },
  ]
  for (const doc of named) await products.create(doc)
  for (let i = 1; i <= 45; i++) await products.create({ name: `Seed Bulk ${String(i).padStart(2, '0')}`, price: i, stock: i, kind: 'tool' })
  await client.auth.logout()
}

// The realm keeps its storage keys as secrets (the local stack's MinIO user).
async function setStorageKeys(url: string) {
  const client = createClient({ url, realm: REALM })
  await client.auth.login({ email: users.admin, password: PASSWORD })
  const have = (await client.admin.secrets.list()).filter((s) => !s.database).map((s) => s.name)
  if (!have.includes('STORAGE_ACCESS_KEY')) await client.admin.secrets.set('STORAGE_ACCESS_KEY', 'backd-dev')
  if (!have.includes('STORAGE_SECRET_KEY')) await client.admin.secrets.set('STORAGE_SECRET_KEY', PASSWORD)
  // A new secret is read within a minute of being set: wait until the storage answers.
  for (let i = 0; i < 90; i++) {
    const check = await client.admin.storage.check().catch(() => null)
    if (check?.ok) break
    await new Promise((r) => setTimeout(r, 1000))
  }
  await client.auth.logout()
}
