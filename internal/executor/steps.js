// The steps a function reports with ctx.step() and ctx.progress(), kept by the
// runner. Plain JavaScript with no Deno APIs, so node can test it.
//
// A step is { n, name, status, started_at, ended_at, duration_ms, current,
// total, message, updated_at }. A new step closes the previous one as done. At
// most MAX_STEPS are kept: the first HEAD and the latest TAIL, and `omitted`
// counts the ones in between.

export const MAX_STEPS = 100;
const HEAD = 50;
const TAIL = 50;
const MAX_NAME = 100;
const MAX_MESSAGE = 200;

const clip = (s, n) => (s.length > n ? s.slice(0, n - 1) + "…" : s);

const count = (what, v) => {
  if (typeof v !== "number" || !Number.isFinite(v) || v < 0) {
    throw new TypeError(`${what} must be a number that is not negative`);
  }
  return v;
};

export function createSteps({ now = () => new Date(), onChange = () => {} } = {}) {
  let n = 0;
  const head = [];
  const tail = [];
  let current = null;

  const stamp = () => now().toISOString();
  const open = () => (current && current.status === "running" ? current : null);
  const close = (status) => {
    const s = open();
    if (!s) return;
    const at = now();
    s.status = status;
    s.ended_at = at.toISOString();
    s.duration_ms = Math.max(0, at.getTime() - new Date(s.started_at).getTime());
    s.updated_at = s.ended_at;
  };

  const api = {
    // Starts a step and closes the one running. total and message are optional.
    step(name, options = {}) {
      if (typeof name !== "string" || name === "") throw new TypeError("ctx.step: the name must be a non-empty string");
      if (options === null || typeof options !== "object") throw new TypeError("ctx.step: options must be an object");
      const total = options.total === undefined ? null : count("ctx.step: total", options.total);
      const message = options.message === undefined ? null : clip(String(options.message), MAX_MESSAGE);
      close("done");
      const at = stamp();
      current = {
        n: ++n, name: clip(name, MAX_NAME), status: "running", started_at: at, ended_at: null, duration_ms: null,
        current: 0, total, message, updated_at: at,
      };
      if (head.length < HEAD) head.push(current);
      else {
        tail.push(current);
        if (tail.length > TAIL) tail.shift();
      }
      onChange("step");
    },

    // Sets how far the current step got. Without a step, one called "progress"
    // starts: reporting must not be what breaks a function.
    progress(value, message) {
      count("ctx.progress: current", value);
      if (!open()) api.step("progress");
      current.current = value;
      if (message !== undefined) current.message = message === null ? null : clip(String(message), MAX_MESSAGE);
      current.updated_at = stamp();
      onChange("progress");
    },

    // The steps so far, as sent to backd: a copy.
    snapshot() {
      return { steps: [...head, ...tail].map((s) => ({ ...s })), omitted: Math.max(0, n - head.length - tail.length) };
    },

    // Ends the running step: done, failed, timed_out or cancelled.
    finish(status = "done") {
      close(status);
    },

    get started() {
      return n > 0;
    },
  };
  return api;
}

// Sends snapshots to backd without a write per update: a progress update is
// sent at most every `interval` ms, a step change at once (but no faster than
// every `minGap` ms, however many steps a loop starts), and flush() sends what
// is pending. Sends never overlap, and a failed one is dropped: reporting is
// best effort and never fails the function.
export function createFlusher({ send, steps, interval = 2000, minGap = 200, clock = Date.now, setTimer = setTimeout, clearTimer = clearTimeout }) {
  let last = -Infinity;
  let timer = null;
  let dirty = false;
  let inflight = Promise.resolve();

  const push = () => {
    timer = null;
    if (!dirty) return inflight;
    dirty = false;
    last = clock();
    const body = steps.snapshot();
    inflight = inflight.then(() => send(body)).catch(() => {});
    return inflight;
  };
  const schedule = (wait) => {
    if (timer !== null) clearTimer(timer);
    timer = setTimer(push, Math.max(0, wait));
  };
  return {
    changed(kind) {
      dirty = true;
      const since = clock() - last;
      schedule(kind === "step" ? minGap - since : interval - since);
    },
    async flush() {
      if (timer !== null) clearTimer(timer);
      await push();
      await inflight;
    },
  };
}
