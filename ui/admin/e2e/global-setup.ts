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
}
