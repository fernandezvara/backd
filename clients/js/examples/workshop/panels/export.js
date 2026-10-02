// Panel 4: a report in the background (export_orders), an async job.
//
// An async function answers at once with 202 and a job; a worker runs it. The
// page polls the job (queued -> running -> done) and then reads the report the
// function wrote. A job runs at least once, so the function derives a key from
// the request and returns the report it already wrote if it runs twice.

/** @param {{ db: any, backd: any }} deps */
export function exportPanel({ db, backd }) {
  return {
    exportRuns: [], // { id, status, rows, reused, csv, error }

    startExport() {
      return this.step('Start the export job', async () => {
        const job = await db.fn('export_orders', {})
        const run = { id: job.id, status: 'queued', rows: null, reused: null, csv: '', error: '' }
        this.exportRuns = [run, ...this.exportRuns]
        const set = (patch) => {
          this.exportRuns = this.exportRuns.map((r) => (r.id === run.id ? { ...r, ...patch } : r))
        }
        // Poll the way job.wait() does, to show each state.
        for (let i = 0; i < 60; i++) {
          const status = await job.status()
          set({ status })
          if (status === 'done') break
          await new Promise((r) => setTimeout(r, 500))
        }
        try {
          const out = await job.wait()
          const report = await db.collection('reports').get(out.report_id)
          set({ status: 'done', rows: out.rows, reused: out.reused, csv: report.csv })
        } catch (err) {
          set({ status: 'done', error: `${err.status ?? ''} ${err.code ?? ''}: ${err.message}` })
        }
      })
    },

    downloadCsv(run) {
      const url = URL.createObjectURL(new Blob([run.csv], { type: 'text/csv' }))
      const a = document.createElement('a')
      a.href = url
      a.download = `orders-${run.id}.csv`
      a.click()
      URL.revokeObjectURL(url)
    },
  }
}
