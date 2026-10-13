/**
 * @typedef {import('./client.js').Client} Client
 * @typedef {import('./client.js').RequestOptions} RequestOptions
 */

/**
 * A device registered for push notifications. Never carries the token.
 * @typedef {object} Device
 * @property {string} id
 * @property {'android' | 'ios' | 'web'} platform
 * @property {string | null} name
 * @property {string} created_at
 * @property {string} last_seen
 */

/**
 * What an app hands over to register a device.
 * @typedef {object} DeviceInput
 * @property {'android' | 'ios' | 'web'} platform
 * @property {string} token  The FCM registration token, the APNs device token or, for `web`, the subscription as JSON.
 * @property {string} [name]  A label to show the user, such as "Ana's Pixel".
 */

/**
 * @typedef {'granted' | 'denied' | 'default' | 'unsupported'} PermissionState
 */

/** @param {string} value */
function base64urlToBytes(value) {
  const padded = value.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - (value.length % 4)) % 4)
  const raw = atob(padded)
  const out = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i)
  return out
}

/**
 * Push notifications (`client.push`): register the devices that should receive the signed-in
 * user's pushes. backd owns the registry, so a token moves to whoever registers it last, and a
 * device goes with the session that registered it: see the docs, Push notifications → Devices.
 */
export class Push {
  /** @type {string | undefined} */
  #deviceId = undefined

  /** @param {Client} client */
  constructor(client) {
    this.client = client
    // A device belongs to a session: once it ends, so does what this client knew of it.
    client.auth.onAuthChange((event) => {
      if (event === 'SIGNED_OUT' || event === 'SESSION_EXPIRED') this.#deviceId = undefined
    })
  }

  /** The id of the device this client registered in this session, if it did. */
  get deviceId() {
    return this.#deviceId
  }

  /**
   * Registers this device, or refreshes it: call it at every launch and after signing in. The
   * answer is the device without its token, and its id is kept for {@link Push#unregister}.
   * Needs a user session (an API key can't register devices).
   * @param {DeviceInput} device
   * @param {RequestOptions} [opts]
   * @returns {Promise<Device>}
   */
  async register({ platform, token, name }, opts) {
    /** @type {Record<string, unknown>} */
    const body = { platform, token }
    if (name !== undefined) body.name = name
    const { data } = await this.client.request({ method: 'POST', path: ['_auth', 'devices'], body, ...opts })
    this.#deviceId = data.id
    return /** @type {Device} */ (data)
  }

  /**
   * The signed-in user's devices, most recently seen first.
   * @param {RequestOptions} [opts]
   * @returns {Promise<Device[]>}
   */
  async devices(opts) {
    const { data } = await this.client.request({ method: 'GET', path: ['_auth', 'devices'], ...opts })
    return data.items
  }

  /**
   * Removes a device: the one this client registered when `id` is not given (nothing happens,
   * and it resolves `false`, when it registered none).
   * @param {string} [id]
   * @param {RequestOptions} [opts]
   * @returns {Promise<boolean>}
   */
  async unregister(id, opts) {
    const target = id ?? this.#deviceId
    if (!target) return false
    await this.client.request({ method: 'DELETE', path: ['_auth', 'devices', target], ...opts })
    if (target === this.#deviceId) this.#deviceId = undefined
    return true
  }

  /**
   * Keeps this device registered: now, if the user is signed in, and after every sign-in. The
   * `getToken` callback returns what to register ({platform, token, name?}), or null when the
   * app has no token yet (permission not given, or none issued). Failures go to `onError`.
   * It is what recovers a realm whose VAPID key was rotated, whose devices were removed.
   * @param {() => Promise<DeviceInput | null> | DeviceInput | null} getToken
   * @param {{ onError?: (err: unknown) => void }} [options]
   * @returns {() => void} Stops registering.
   */
  registerOnLaunch(getToken, options = {}) {
    let active = true
    const run = async () => {
      try {
        const input = await getToken()
        if (active && input) await this.register(input)
      } catch (err) {
        options.onError?.(err)
      }
    }
    const stop = this.client.auth.onAuthChange((event) => {
      if (event === 'SIGNED_IN') void run()
    })
    void this.client.auth.token().then((token) => {
      if (token && active) return run()
    })
    return () => {
      active = false
      stop()
    }
  }

  /**
   * Subscribes this browser to push and registers the subscription: fetches the realm's public
   * key, asks for the permission (unless `requestPermission` is false), calls
   * `pushManager.subscribe({ userVisibleOnly: true, applicationServerKey })` on the service
   * worker's registration and registers the result as a `web` device. A denied permission is
   * not an error: nothing is subscribed, and the answer says so.
   * @param {object} options
   * @param {{ pushManager: { subscribe(options: any): Promise<{ toJSON(): unknown }> } }} options.registration  The service worker registration (`navigator.serviceWorker.ready`).
   * @param {string} [options.name]
   * @param {boolean} [options.requestPermission]  Ask for the permission when it is not decided; default true.
   * @param {{ permission: string, requestPermission(): Promise<string> }} [options.notification]  `Notification`; for tests.
   * @param {RequestOptions} [opts]
   * @returns {Promise<{ permission: PermissionState, device: Device | null }>}
   */
  async subscribeWeb({ registration, name, requestPermission = true, notification }, opts) {
    const api = notification ?? (typeof Notification === 'undefined' ? undefined : Notification)
    if (!api || !registration?.pushManager) return { permission: 'unsupported', device: null }
    let permission = /** @type {PermissionState} */ (api.permission)
    if (permission === 'default' && requestPermission) permission = /** @type {PermissionState} */ (await api.requestPermission())
    if (permission !== 'granted') return { permission, device: null }
    const { data } = await this.client.request({ method: 'GET', path: ['_auth', 'devices', 'web-key'], auth: false, ...opts })
    const subscription = await registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: base64urlToBytes(data.public_key) })
    const device = await this.register({ platform: 'web', token: JSON.stringify(subscription.toJSON()), name }, opts)
    return { permission, device }
  }
}
