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
    // The password form, the second step and the second factor's settings.
    form: { email: '', password: '' },
    step: null, // { challenge, methods } waiting for a code, or { enroll: true, challenge }
    code: '',
    mfa: null, // the status of the signed-in user's second factor
    setup: null, // { setup_id, secret, otpauth_uri, qr } while setting an app up
    recovery: [], // shown once
    mailLink: { email: '', sent: false },

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
      // A sign-in that came back asking for a code (a provider's, or a magic link's).
      if (this.user === null && this.step === null) {
        const back = await backd.auth.ready.catch(() => null)
        if (back?.mfaRequired || back?.enrollmentRequired) this.step = back
      }
      if (await backd.auth.hasSession()) {
        try {
          this.user = await backd.auth.me()
          this.mfa = await backd.auth.mfa.status()
        } catch {
          // The stored session expired; onAuthChange handles it.
        }
      }
    },

    // Email and password. A user with a second factor gets a step instead of a session.
    signInWithPassword() {
      return this.run(async () => {
        this.notice = ''
        const result = await backd.auth.signIn({ email: this.form.email, password: this.form.password })
        this.form.password = ''
        if ('challenge' in result) this.step = result
        else await this.signedIn(result)
      })
    },

    // The second step: the code of the authenticator app, or a recovery code (xxxxx-xxxxx).
    submitCode() {
      return this.run(async () => {
        const method = this.code.includes('-') ? 'recovery_code' : 'totp'
        const session = await backd.auth.verifyMfa(this.step.challenge, method, this.code.trim())
        this.code = ''
        this.step = null
        await this.signedIn(session)
      })
    },

    async signedIn(session) {
      this.notice = 'Signed in.'
      this.user = await backd.auth.me()
      this.mfa = await backd.auth.mfa.status()
      return session
    },

    // A role that requires a second factor sets it up before the session exists.
    startEnrollment() {
      return this.run(async () => {
        this.setup = await backd.auth.mfa.setup({ challenge: this.step.challenge })
      })
    },

    finishEnrollment() {
      return this.run(async () => {
        const done = await backd.auth.mfa.confirm({ challenge: this.step.challenge, setupId: this.setup.setup_id, code: this.code.trim() })
        this.code = ''
        this.recovery = done.recovery_codes
        this.step = null
        this.setup = null
        await this.signedIn(done.session)
      })
    },

    // For a signed-in user: a fresh proof (their password here), then the setup.
    enableMfa() {
      return this.run(async () => {
        const pw = prompt('Your password, to confirm it is you:')
        if (!pw) return
        await backd.auth.reauth({ method: 'password', password: pw })
        this.setup = await backd.auth.mfa.setup()
      })
    },

    confirmMfa() {
      return this.run(async () => {
        const done = await backd.auth.mfa.confirm({ setupId: this.setup.setup_id, code: this.code.trim() })
        this.code = ''
        this.recovery = done.recovery_codes
        this.setup = null
        this.mfa = await backd.auth.mfa.status()
      })
    },

    // A message with a link and a code (the realm needs magic_link and email).
    sendLink() {
      return this.run(async () => {
        await backd.auth.magicLink.request(this.mailLink.email)
        this.mailLink.sent = true
      })
    },

    // The code of the message, typed in this page.
    submitLinkCode() {
      return this.run(async () => {
        const result = await backd.auth.magicLink.verifyCode(this.code)
        this.code = ''
        this.mailLink.sent = false
        if ('challenge' in result) this.step = result
        else await this.signedIn(result)
      })
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
