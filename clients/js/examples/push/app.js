// Subscribing a browser to push with backd-js, after the user signed in.
//
//   <button id="enable">Turn notifications on</button>
//   <script type="module" src="app.js"></script>
//
// The realm needs a `web` platform with provider `webpush` (and `backd push web-keys` run once),
// and sw.js served from this origin. Browsers want a user gesture to ask for the permission, so
// subscribe from a click, not at load.
import { createClient } from 'backd-js'

const backd = createClient({ url: window.location.origin, realm: 'acme' })

document.getElementById('enable').addEventListener('click', async () => {
  const registration = await navigator.serviceWorker.register('/sw.js')
  await navigator.serviceWorker.ready
  // Asks for the permission, subscribes with the realm's key and registers the subscription.
  // A denied permission is an answer, not an error: nothing is subscribed.
  const { permission, device } = await backd.push.subscribeWeb({ registration, name: navigator.userAgent.slice(0, 60) })
  if (permission !== 'granted') {
    console.log(`notifications are ${permission}`)
    return
  }
  console.log('registered', device.id)

  // Registered devices belong to the session: sign out and they stop. After a sign-in (or when
  // the realm's key was rotated and its web devices removed) subscribe again at launch:
  backd.push.registerOnLaunch(async () => {
    const existing = await registration.pushManager.getSubscription()
    return existing ? { platform: 'web', token: JSON.stringify(existing.toJSON()) } : null
  })
})
