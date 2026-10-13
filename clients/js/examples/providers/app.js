// The providers example: Alpine.js + backd-js, no build step.
// One button per provider configured in examples/config/idtest/realm.yaml.
// The button starts the redirect flow; backd sends the user back to this
// page with a one-time code that the client redeems for a session on its own
// (backd.auth.ready is the outcome).
// Signed in, the page lists the user's sign-in methods and can link or
// unlink providers. See design/identity-providers.md and its testing guide.
import { createClient, localStorageStorage } from 'backd-js'

const backd = createClient({
  // The page and the API share one origin (nginx in docker-compose.yml);
  // serve it behind a tunnel for Apple (see examples/config/idtest/realm.yaml).
  url: window.location.origin,
  realm: 'idtest',
  storage: localStorageStorage('backd-providers-example'),
})
// The providers configured in examples/config/idtest/realm.yaml. A button for a
// provider whose secret is not set fails at the start with provider_unavailable.
const PROVIDERS = ['google', 'microsoft', 'apple']

document.addEventListener('alpine:init', () => {
  window.Alpine.data('app', () => ({
    user: null,
    providers: PROVIDERS,
    notice: '',
    error: '',
    busy: false,

    async init() {
      backd.auth.onAuthChange((event) => {
        if (event === 'SESSION_EXPIRED') {
          this.user = null
          this.error = 'Your session ended. Please sign in again.'
        }
      })
      // Back from a provider: the client finished the sign-in (or linking) the buttons
      // started when it was created; `ready` is its outcome (null on an ordinary visit).
      try {
        const result = await backd.auth.ready
        if (result?.linked) {
          this.notice = `Linked ${result.linked}: it now signs in as you.`
        } else if (result) {
          this.notice = result.new_user
            ? `Welcome! Signed up${result.profile?.name ? ' as ' + result.profile.name : ''}.`
            : 'Signed in.'
          this.user = result.user
        }
      } catch (err) {
        this.error = describe(err)
      }
      if (await backd.auth.hasSession()) {
        try {
          this.user = await backd.auth.me()
        } catch {
          // The stored session expired; onAuthChange handles it.
        }
      }
    },

    // Sends the browser to the provider; the client finishes it when the page comes back.
    continueWith(provider) {
      return this.run(async () => {
        this.notice = ''
        await backd.auth.signInWith(provider)
      })
    },

    // Adds a provider as another way to sign in to this account.
    link(provider) {
      return this.run(async () => {
        this.notice = ''
        await backd.auth.linkProvider(provider)
      })
    },

    unlink(provider) {
      return this.run(async () => {
        this.notice = ''
        await backd.auth.unlinkProvider(provider)
        this.user = await backd.auth.me()
        this.notice = `Removed ${provider}.`
      })
    },

    linked(provider) {
      return (this.user?.identities ?? []).some((i) => i.provider === provider)
    },

    logout() {
      return this.run(async () => {
        await backd.auth.logout()
        this.user = null
      })
    },

    // Runs an action, showing its error if it fails.
    async run(action) {
      this.error = ''
      this.busy = true
      try {
        await action()
      } catch (err) {
        this.error = describe(err)
      } finally {
        this.busy = false
      }
    },
  }))
})

// A readable message for each outcome the flow can end with
// (design/identity-providers.md §8), plus validation details.
function describe(err) {
  const friendly = {
    cancelled: 'The sign-in was cancelled at the provider.',
    account_exists: 'An account with that address already exists: sign in with it first, then link the provider.',
    signup_closed: 'This realm does not accept new accounts.',
    invitation_invalid: 'The invitation is not valid for this sign-in.',
    link_conflict: 'That provider account is already linked (to this or another user).',
    email_not_verified: 'The provider did not assert a verified address; verify it first.',
    email_required: 'The provider account has no email address; this realm needs one.',
    signin_refused: 'This account cannot sign in.',
    provider_unavailable: 'This provider is not configured yet: set its ids and secrets (see examples/config/idtest/realm.yaml).',
    provider_error: 'The provider refused the sign-in; check the backd log for why.',
    sign_in_not_started: 'No sign-in is waiting in this tab: start it with the buttons below.',
  }
  const fields = (err.details ?? []).map((d) => `${d.path} ${d.reason}`)
  return [friendly[err.code] ?? err.message, ...fields].join(' · ')
}
