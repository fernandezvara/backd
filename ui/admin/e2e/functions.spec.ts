import { expect, test } from './fixtures'
import { users } from './users'

test('definitions show each function with its schedule, mode and limits', async ({ page, signIn }) => {
  await signIn('admin')
  await page.getByRole('link', { name: 'Functions' }).click()
  const greet = page.getByTestId('fn-main-greet')
  await expect(greet).toContainText('sync')
  await expect(greet).toContainText('Internal')
  await expect(greet).toContainText('Not scheduled')
  await expect(page.getByTestId('fn-main-report')).toContainText('async')
  await expect(page.getByTestId('fn-main-nightly')).toContainText('0 3 * * *')
  await expect(greet).toContainText('adminui/main/_functions/greet/function.yaml')
})

test('a schedule can be paused and resumed, and the state is kept', async ({ page, signIn }) => {
  await signIn('admin', { keep: true }) // the token must outlive the reload below
  await page.getByRole('link', { name: 'Functions' }).click()
  const nightly = page.getByTestId('fn-main-nightly')
  // The state is the server's, so a retry may find the schedule paused: start running.
  const resume = nightly.getByRole('button', { name: 'Resume' })
  if (await resume.isVisible()) await resume.click()
  await expect(nightly.getByRole('button', { name: 'Pause' })).toBeVisible()
  await expect(nightly).not.toContainText('Paused')

  await nightly.getByRole('button', { name: 'Pause' }).click()
  await expect(page.getByText('Schedule of main/nightly paused.')).toBeVisible()
  await expect(nightly).toContainText('Paused')

  // The state is the server's: it survives a reload.
  await page.reload()
  await expect(page.getByTestId('fn-main-nightly')).toContainText('Paused')

  await page.getByTestId('fn-main-nightly').getByRole('button', { name: 'Resume' }).click()
  await expect(page.getByText('Schedule of main/nightly resumed.')).toBeVisible()
  await expect(page.getByTestId('fn-main-nightly')).not.toContainText('Paused')
  // An unscheduled function has no such button.
  await expect(page.getByTestId('fn-main-greet').getByRole('button', { name: 'Pause' })).toHaveCount(0)
})

test('running by hand: a sync function answers, an async one queues a job, and both are recorded', async ({ page, signIn }) => {
  await signIn('admin')
  await page.getByRole('link', { name: 'Functions' }).click()

  // Bad JSON is refused before anything is sent.
  await page.getByRole('button', { name: 'Run a function' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Function', { exact: true }).selectOption('main/greet')
  await dialog.getByLabel('Input (JSON)').fill('{nope')
  await dialog.getByRole('button', { name: 'Run', exact: true }).click()
  await expect(dialog.getByText("The input isn't valid JSON.")).toBeVisible()

  // As a user: ctx.user is that user.
  await dialog.getByLabel('Input (JSON)').fill('{"name":"Ada"}')
  await dialog.getByLabel('Run as (email)').fill(users.viewer)
  await dialog.getByRole('button', { name: 'Run', exact: true }).click()
  const out = page.getByTestId('last-output')
  await expect(out).toContainText('hello Ada')
  await expect(out).toContainText(users.viewer)

  // An async function: a job is queued.
  await page.getByTestId('fn-main-report').getByRole('button', { name: 'Run' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Run', exact: true }).click()
  await expect(page.getByTestId('last-run')).toContainText('An async job was queued')
  await page.getByRole('link', { name: 'See the job' }).click()
  await expect(page).toHaveURL(/functions\/jobs/)
  const row = page.getByTestId('jobs-table').locator('tbody tr').first()
  await expect(row).toContainText('main/report')
  await expect(row).toContainText('admin')
  await expect(async () => {
    await page.getByRole('button', { name: 'Filter' }).click()
    await expect(row).toContainText('done')
  }).toPass({ timeout: 30_000 })
  await expect(row).toContainText('ok')

  // The history has both runs; the sync one's log line is there, as text.
  await page.getByRole('link', { name: 'History' }).click()
  await expect(page.getByText('Logs hold whatever the functions printed')).toBeVisible()
  await page.getByRole('textbox', { name: 'Function' }).fill('main/greet')
  await page.getByRole('button', { name: 'Filter' }).click()
  const first = page.getByTestId('history-table').locator('tbody tr').first()
  await expect(first).toContainText('main/greet')
  await expect(first).toContainText('ok')
  await first.getByText('Details').click()
  await expect(first.getByTestId('log-lines')).toContainText('[log] greeting Ada')
})

test('a read-only administrator reads everything and runs nothing', async ({ page, signIn }) => {
  await signIn('viewer')
  await page.getByRole('link', { name: 'Functions' }).click()
  await expect(page.getByTestId('fn-main-greet')).toBeVisible()
  await expect(page.getByRole('button', { name: /^Run/ })).toHaveCount(0)
  await page.getByRole('link', { name: 'History' }).click()
  await expect(page.getByTestId('history-table')).toBeVisible()
  await page.getByRole('link', { name: 'Jobs' }).click()
  await expect(page.getByTestId('jobs-table')).toBeVisible()
})

test('a custom level without the functions area has no way in', async ({ page, signIn }) => {
  await signIn('support')
  await expect(page.getByRole('link', { name: 'Users' })).toBeVisible()
  await expect(page.getByRole('link', { name: 'Functions' })).toHaveCount(0)
})

test('the functions views have no accessibility violations', async ({ page, signIn }) => {
  const { default: AxeBuilder } = await import('@axe-core/playwright')
  await signIn('admin')
  for (const theme of ['Light', 'Dark']) {
    await page.getByLabel('Theme').selectOption({ label: theme })
    await page.getByRole('link', { name: 'Functions' }).click()
    await expect(page.getByTestId('fn-main-greet')).toBeVisible()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await page.getByRole('button', { name: 'Run a function' }).click()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await page.keyboard.press('Escape')
    await page.getByRole('link', { name: 'History' }).click()
    await expect(page.getByTestId('history-table')).toBeVisible()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await page.getByRole('link', { name: 'Jobs' }).click()
    await expect(page.getByTestId('jobs-table')).toBeVisible()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  }
})

test('a running job shows the steps it reported, and a cancel closes the current one', async ({ page, signIn }) => {
  await signIn('admin', { keep: true })
  await page.getByRole('link', { name: 'Functions' }).click()
  await page.getByTestId('fn-main-slow').getByRole('button', { name: 'Run' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Run', exact: true }).click()
  await page.getByRole('link', { name: 'See the job' }).click()
  const row = page.getByTestId('jobs-table').locator('tbody tr').first()
  const jobId = (await row.locator('td').first().innerText()).trim()

  // The function reports "warm up", then "count" (5 items): the listing shows the current step.
  await expect(async () => {
    await page.getByRole('button', { name: 'Filter' }).click()
    await expect(row).toContainText('count', { timeout: 2000 })
  }).toPass({ timeout: 40_000 })

  // Opening it lists every step, with the first one closed.
  const progress = page.getByTestId(`progress-${jobId}`)
  await progress.locator('summary').click()
  await expect(progress.getByTestId('step-1')).toContainText('warm up')
  await expect(progress.getByTestId('step-1')).toContainText('done')
  await expect(progress.getByTestId('step-2')).toContainText('count')
  await expect(progress.getByTestId('step-2')).toContainText('running')
  await expect(progress.getByTestId('step-2')).toContainText('/ 5')

  // Cancelling closes the step the job was on.
  await row.getByRole('button', { name: `Cancel ${jobId}` }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Cancel the job' }).click()
  await expect(row).toContainText('cancelled')
  // The steps still open now show the closed step, with no new click.
  await expect(page.getByTestId(`progress-${jobId}`).getByTestId('step-2')).toContainText('cancelled')

  // The attempt's record keeps the steps next to its logs.
  await page.getByRole('link', { name: 'History' }).click()
  await page.getByRole('textbox', { name: 'Function' }).fill('main/slow')
  await expect(async () => {
    await page.getByRole('button', { name: 'Filter' }).click()
    await page.getByTestId('history-table').locator('tbody tr').first().locator('summary').click({ timeout: 2000 })
    await expect(page.getByTestId('history-table').getByTestId('step-2').first()).toContainText('cancelled', { timeout: 2000 })
  }).toPass({ timeout: 20_000 })
})

test('a running job is cancelled, and a finished one re-run', async ({ page, signIn }) => {
  await signIn('admin', { keep: true })
  await page.getByRole('link', { name: 'Functions' }).click()

  // Start a slow job by hand and wait until a worker is running it.
  await page.getByTestId('fn-main-slow').getByRole('button', { name: 'Run' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Run', exact: true }).click()
  await page.getByRole('link', { name: 'See the job' }).click()
  await expect(page).toHaveURL(/functions\/jobs/)
  const row = page.getByTestId('jobs-table').locator('tbody tr').first()
  await expect(row).toContainText('main/slow')
  await expect(async () => {
    await page.getByRole('button', { name: 'Filter' }).click()
    await expect(row).toContainText('running')
  }).toPass({ timeout: 30_000 })

  // Cancel it: done at once, with the result cancelled.
  const jobId = (await row.locator('td').first().innerText()).trim()
  await row.getByRole('button', { name: `Cancel ${jobId}` }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Cancel the job' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Job cancelled.' })).toBeVisible()
  await expect(row).toContainText('done')
  await expect(row).toContainText('cancelled')
  await expect(row.getByRole('button', { name: /^Cancel/ })).toHaveCount(0)

  // The worker stops the run: its attempt is recorded as cancelled, long before the function would have ended.
  await page.getByRole('link', { name: 'History' }).click()
  await page.getByRole('textbox', { name: 'Function' }).fill('main/slow')
  await expect(async () => {
    await page.getByRole('button', { name: 'Filter' }).click()
    await expect(page.getByTestId('history-table').locator('tbody tr').first()).toContainText('cancelled', { timeout: 2000 })
  }).toPass({ timeout: 20_000 })

  // Re-run a finished job (the cancelled one itself): a new job that links back.
  await page.getByRole('link', { name: 'Jobs' }).click()
  await page.getByRole('textbox', { name: 'Function' }).fill('main/slow')
  await page.getByRole('button', { name: 'Filter' }).click()
  await page.getByRole('button', { name: `Re-run ${jobId}` }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Re-run', exact: true }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Queued as job' })).toBeVisible()
  const first = page.getByTestId('jobs-table').locator('tbody tr').first()
  await expect(first).toContainText(`Re-run of ${jobId}`)
  await expect(first).toContainText('admin')
  // Don't leave it running for the next tests.
  await expect(async () => {
    await page.getByRole('button', { name: 'Filter' }).click()
    await first.getByRole('button', { name: /^Cancel/ }).click({ timeout: 2000 })
  }).toPass({ timeout: 20_000 })
  await page.getByRole('dialog').getByRole('button', { name: 'Cancel the job' }).click()
  await expect(first).toContainText('cancelled')
})

test('a read-only administrator sees jobs and schedules with no way to cancel, re-run or pause', async ({ page, signIn }) => {
  await signIn('viewer', { keep: true })
  await page.getByRole('link', { name: 'Functions' }).click()
  await expect(page.getByTestId('fn-main-nightly')).toContainText('0 3 * * *')
  await expect(page.getByRole('button', { name: /^(Pause|Resume)$/ })).toHaveCount(0)
  await page.getByRole('link', { name: 'Jobs' }).click()
  await expect(page.getByTestId('jobs-table')).toBeVisible()
  await expect(page.getByRole('button', { name: /^(Cancel|Re-run)/ })).toHaveCount(0)
})
