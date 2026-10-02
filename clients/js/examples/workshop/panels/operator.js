// Panel 5: operating it, as an administrator (the operator demo account).
//
// What an operator needs to see about functions that run without anyone
// watching: the secrets that are set (never their values), the async and
// scheduled jobs with their attempts, the history of every call with where it
// came from, and a way to run a function by hand. Everything here is the
// realm's admin API, used with the operator's session.

/** @param {{ backd: any }} deps */
export function operatorPanel({ backd }) {
  return {
    ops: { secrets: [], jobs: [], history: [], loaded: false },
    jobFilter: 'all', // all | scheduled
    chain: null, // { request_id, rows: [{ ...record, depth }] }
    manual: [], // { label, status, text }

    refreshOps() {
      return this.step('Operator: refresh secrets, jobs and history', async () => {
        const jobParams = { limit: 15 }
        if (this.jobFilter === 'scheduled') jobParams.scheduled = true
        const [secrets, jobs, history] = await Promise.all([
          backd.admin.secrets.list(),
          backd.admin.jobs.list(jobParams),
          backd.admin.invocations.list({ limit: 20 }),
        ])
        this.ops = { secrets, jobs: jobs.items, history: history.items, loaded: true }
      })
    },

    // Runs a function by hand through the admin API: the way to re-run a
    // clean-up, or to test a scheduled function without waiting for the clock.
    runByHand(label, name, input = null) {
      return this.step(`Operator: run ${name} by hand`, async () => {
        const run = { id: `${Date.now()}-${Math.random()}`, label, status: 'queued', text: '' }
        this.manual = [run, ...this.manual]
        const set = (patch) => {
          this.manual = this.manual.map((m) => (m.id === run.id ? { ...m, ...patch } : m))
        }
        try {
          const job = await backd.admin.invokeFunction(`main/${name}`, { input })
          const output = await job.wait({ pollIntervalMs: 400, timeoutMs: 30000 })
          set({ status: 'done', text: JSON.stringify(output) })
        } catch (err) {
          set({ status: 'failed', text: `${err.status ?? ''} ${err.code ?? ''} ${err.message}`.trim() })
        }
        await this.refreshOpsQuiet()
      })
    },

    async refreshOpsQuiet() {
      const [jobs, history] = await Promise.all([backd.admin.jobs.list({ limit: 15 }), backd.admin.invocations.list({ limit: 20 })])
      this.ops = { ...this.ops, jobs: jobs.items, history: history.items }
    },

    // Every call of one request, as a tree by parent_id: who called whom.
    showChain(record) {
      return this.step(`Operator: the calls of request ${record.request_id}`, async () => {
        const page = await backd.admin.invocations.list({ requestId: record.request_id, limit: 50 })
        const items = [...page.items].sort((a, b) => a.at.localeCompare(b.at))
        const children = new Map()
        for (const r of items) {
          const key = r.parent_id ?? ''
          children.set(key, [...(children.get(key) ?? []), r])
        }
        const rows = []
        const walk = (parent, depth) => {
          for (const r of children.get(parent) ?? []) {
            rows.push({ ...r, depth })
            walk(r.id, depth + 1)
          }
        }
        walk('', 0)
        // Records whose parent isn't in the list are shown at the top.
        for (const r of items) if (!rows.some((x) => x.id === r.id)) rows.push({ ...r, depth: 0 })
        this.chain = { request_id: record.request_id, rows }
      })
    },

    time(at) {
      return new Date(at).toLocaleTimeString()
    },
    today() {
      return new Date().toISOString().slice(0, 10)
    },
  }
}
