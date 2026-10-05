// Idle timeout: calls `onIdle` once nothing has touched the page for
// `seconds`. Any pointer, key, scroll or touch counts as activity.

const EVENTS = ['pointerdown', 'pointermove', 'keydown', 'wheel', 'scroll', 'touchstart'] as const

export interface IdleWatch {
  stop(): void
}

export function watchIdle(seconds: number, onIdle: () => void, target: Window = window): IdleWatch {
  let timer: ReturnType<typeof setTimeout> | undefined
  let last = Date.now()
  const arm = () => {
    clearTimeout(timer)
    timer = setTimeout(check, seconds * 1000)
  }
  // Timers are throttled in background tabs, so compare clocks too.
  const check = () => {
    if (Date.now() - last >= seconds * 1000) {
      stop()
      onIdle()
    } else {
      arm()
    }
  }
  const touch = () => {
    last = Date.now()
  }
  const visible = () => {
    if (!target.document.hidden) check()
  }
  for (const e of EVENTS) target.addEventListener(e, touch, { passive: true })
  target.document.addEventListener('visibilitychange', visible)
  function stop() {
    clearTimeout(timer)
    for (const e of EVENTS) target.removeEventListener(e, touch)
    target.document.removeEventListener('visibilitychange', visible)
  }
  arm()
  return { stop }
}
