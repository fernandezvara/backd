// A service worker for web push: shows the notification a push carries and opens its `url`
// when it is tapped. backd's Web Push sender delivers { title, body, url, data, tag } as JSON
// (docs: Push notifications -> Web Push). Serve this file from your app's origin, at the scope
// you want pushes to reach (usually the root: /sw.js).

self.addEventListener('push', (event) => {
  let message = {}
  try {
    message = event.data ? event.data.json() : {}
  } catch {
    message = { title: 'New notification', body: event.data ? event.data.text() : '' }
  }
  const { title = 'New notification', body = '', url, data, tag } = message
  // `tag` is the push's `group`: a newer push with the same tag replaces the earlier one.
  event.waitUntil(self.registration.showNotification(title, { body, tag, data: { url, ...data } }))
})

self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const url = event.notification.data?.url
  if (!url) return
  event.waitUntil(
    self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((windows) => {
      // Focus a tab that already shows the page; open one otherwise.
      for (const w of windows) {
        if (w.url === new URL(url, self.location.origin).href && 'focus' in w) return w.focus()
      }
      return self.clients.openWindow(url)
    }),
  )
})
