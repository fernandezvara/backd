import { test } from "node:test";
import assert from "node:assert/strict";
import { createFlusher, createSteps } from "./steps.js";

const clockAt = (start = Date.UTC(2026, 9, 6, 10, 0, 0)) => {
  let t = start;
  return { now: () => new Date(t), tick: (ms) => (t += ms) };
};

test("a step closes the previous one, and progress updates the current one", () => {
  const c = clockAt();
  const s = createSteps({ now: c.now });
  s.step("load", { total: 10, message: "reading" });
  c.tick(1500);
  s.progress(4, "four");
  c.tick(500);
  s.step("save");
  const { steps, omitted } = s.snapshot();
  assert.equal(omitted, 0);
  assert.deepEqual(steps.map((x) => [x.n, x.name, x.status]), [[1, "load", "done"], [2, "save", "running"]]);
  assert.equal(steps[0].current, 4);
  assert.equal(steps[0].total, 10);
  assert.equal(steps[0].message, "four");
  assert.equal(steps[0].duration_ms, 2000);
  assert.equal(steps[1].ended_at, null);
  s.finish("failed");
  assert.equal(s.snapshot().steps[1].status, "failed");
});

test("progress without a step starts one, and bad arguments throw", () => {
  const s = createSteps();
  s.progress(3);
  assert.equal(s.snapshot().steps[0].name, "progress");
  assert.throws(() => s.step(""), TypeError);
  assert.throws(() => s.step("x", { total: -1 }), TypeError);
  assert.throws(() => s.progress(Number.NaN), TypeError);
});

test("at most 100 steps are kept: the first 50 and the latest 50", () => {
  const s = createSteps();
  for (let i = 1; i <= 250; i++) s.step(`s${i}`);
  const { steps, omitted } = s.snapshot();
  assert.equal(steps.length, 100);
  assert.equal(omitted, 150);
  assert.equal(steps[0].name, "s1");
  assert.equal(steps[49].name, "s50");
  assert.equal(steps[50].name, "s201");
  assert.equal(steps[99].name, "s250");
  assert.equal(steps[99].status, "running");
});

test("text is clipped", () => {
  const s = createSteps();
  s.step("n".repeat(500), { message: "m".repeat(5000) });
  const [step] = s.snapshot().steps;
  assert.ok(step.name.length <= 100 && step.message.length <= 200);
});

test("thousands of progress updates make a bounded number of sends", async () => {
  let t = 0;
  const timers = [];
  const sent = [];
  let flusher;
  const steps = createSteps({ onChange: (k) => flusher.changed(k) });
  flusher = createFlusher({
    steps, send: async (b) => { sent.push(b); }, clock: () => t,
    setTimer: (fn, ms) => timers.push({ fn, at: t + ms }) - 1,
    clearTimer: (i) => { timers[i].fn = null; },
  });
  // Runs the timers that are due, as the event loop would.
  const advance = async (to) => {
    for (const x of timers) if (x.fn && x.at <= to) { const fn = x.fn; x.fn = null; t = Math.max(t, x.at); await fn(); }
    t = Math.max(t, to);
  };
  steps.step("work", { total: 5000 });
  await advance(t + 300);
  for (let i = 1; i <= 5000; i++) { t += 1; steps.progress(i); }
  await advance(t + 10_000);
  assert.ok(sent.length <= 4, `${sent.length} sends for 5000 updates in 5 s`);
  assert.equal(sent.at(-1).steps[0].current, 5000);

  // The last value is flushed when the call ends, and a step change goes out at once.
  steps.progress(5001);
  steps.step("next");
  await advance(t + 300);
  steps.progress(5002);
  await flusher.flush();
  assert.equal(sent.at(-1).steps.at(-1).name, "next");
  assert.equal(sent.at(-1).steps.at(-1).current, 5002);
});

test("a failing send never throws", async () => {
  let flusher;
  const steps = createSteps({ onChange: (k) => flusher.changed(k) });
  flusher = createFlusher({ steps, send: async () => { throw new Error("down"); } });
  steps.step("x");
  await flusher.flush();
});
