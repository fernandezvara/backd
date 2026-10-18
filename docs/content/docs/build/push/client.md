---
title: "From apps"
description: "client.push in the JavaScript and Dart clients: register a device, keep it registered, subscribe a browser, and what other apps do with the raw endpoints."
icon: "device-mobile"
weight: 588
toc: true
---

The [JavaScript client](../../clients/js/) has `client.push`, a small layer over the [device endpoints](../devices/). It needs a **user session**: an API key can't register devices.

```js
const device = await backd.push.register({ platform: 'android', token: fcmToken, name: "Ana's Pixel" })
const mine = await backd.push.devices()           // [{ id, platform, name, created_at, last_seen }] (never a token)
await backd.push.unregister()                     // the device this client registered; or unregister(id)
```

| Call | What it does |
|---|---|
| `register({ platform, token, name? })` | `POST /_auth/devices`. Registers or refreshes; keeps the returned id (`client.push.deviceId`) for `unregister()` |
| `devices()` | `GET /_auth/devices` |
| `unregister(id?)` | `DELETE /_auth/devices/{id}`; without an id, the device this client registered (`false` when there is none) |
| `registerOnLaunch(getToken, { onError })` | Registers **now** if signed in and again **after every sign-in**. `getToken` returns what to register, or `null` while the app has no token. Returns a function that stops it |
| `subscribeWeb({ registration, name?, requestPermission? })` | Subscribes a browser and registers the subscription (below) |

## Keeping a device registered

A device is removed when its session ends, when it is unseen for `push.devices.stale_after`, and when its realm's web key is rotated. So an app registers **at every launch and after every sign-in**, which is what `registerOnLaunch` does:

```js
const stop = backd.push.registerOnLaunch(async () => {
  const token = await getFcmToken()               // your platform's call
  return token ? { platform: 'android', token } : null
}, { onError: (e) => console.warn('push registration failed', e) })
```

## A browser

`subscribeWeb` runs the whole sequence: it fetches the realm's public key, asks for the notification permission, calls `pushManager.subscribe({ userVisibleOnly: true, applicationServerKey })` and registers the subscription as a `web` device.

```js
const registration = await navigator.serviceWorker.register('/sw.js')
const { permission, device } = await backd.push.subscribeWeb({ registration })
// permission: 'granted' | 'denied' | 'default' | 'unsupported'; device is null unless granted
```

A **denied** permission isn't an error: nothing is subscribed and the answer says so. Ask from a user gesture (a click), as browsers require.

### The service worker

The push arrives in a service worker, which shows the notification and opens its link when it is tapped. This one is the example the repository carries (`clients/js/examples/push/sw.js`): backd's sender delivers `{ title, body, url, data, tag }`.

```js
self.addEventListener('push', (event) => {
  const { title = 'New notification', body = '', url, data, tag } = event.data ? event.data.json() : {}
  event.waitUntil(self.registration.showNotification(title, { body, tag, data: { url, ...data } }))
})

self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const url = event.notification.data?.url
  if (url) event.waitUntil(self.clients.openWindow(url))
})
```

`tag` is the push's `group`: a newer push with the same tag replaces the earlier one.

## Mobile and desktop apps

The [Dart client](../../clients/dart/) has the same `client.push` (`register`, `devices`, `unregister`, `registerOnLaunch`) for Flutter and standalone Dart. An app written without either client calls the endpoints itself, at each launch and after signing in:

```http
POST /v1/acme/_auth/devices
Authorization: Bearer <session token>
Content-Type: application/json

{"platform": "ios", "token": "<APNs device token>", "name": "Ana's iPhone"}
```

`android` takes the FCM registration token; `ios` the APNs device token (or the FCM token when the realm's `ios` provider is `fcm`). See [Devices](../devices/) for the endpoints and their rules.

## The example

The **expenses with functions** example sends a push when the receiver of a settlement confirms it: `confirm_settlement` declares `push: { kinds: [settlement-confirmed] }` and calls `ctx.push.send({ user: entry._meta.owner, kind: "settlement-confirmed", … })`. Its realm uses the `capture` provider, and the page registers a demo device with `registerOnLaunch`, so the push shows up in the Mailbox's **Push** tab ([Local development](../../../operate/providers/push/local/)).
