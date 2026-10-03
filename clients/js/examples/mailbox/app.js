// The mailbox: lists the emails a realm's `email-capture` function stored in
// its `outbox` collection (docs: Functions -> Email) and shows them, links
// included, so verification and password reset can be clicked through by
// hand. Local development only: the outbox is readable by anyone.
//   /example/mailbox/?realm=workshop&database=notifications
import { createClient } from 'backd-js'

const params = new URLSearchParams(window.location.search)
const realm = params.get('realm') ?? 'workshop'
const database = params.get('database') ?? 'notifications'
const backd = createClient({ url: window.location.origin, realm })
const outbox = backd.db(database).collection('outbox')

const $ = (id) => document.getElementById(id)
$('realm').textContent = realm

let emails = []
let selected = null

function show(email) {
  selected = email?.id ?? null
  $('empty').hidden = email !== undefined && email !== null
  $('message').hidden = !email
  if (email) {
    $('to').textContent = email.to.join(', ')
    $('subject').textContent = email.subject
    $('kind').textContent = email.kind
    const link = $('open-link')
    link.hidden = !email.link
    if (email.link) link.href = email.link
    // Links in the HTML open in a new tab, outside the sandboxed frame.
    $('html').srcdoc = `<base target="_blank">${email.html}`
    $('text').textContent = email.text
  }
  render()
}

function render() {
  const list = $('list')
  list.replaceChildren(
    ...emails.map((email) => {
      const li = document.createElement('li')
      const button = document.createElement('button')
      button.type = 'button'
      button.className = 'secondary'
      if (email.id === selected) button.setAttribute('aria-current', 'true')
      const subject = document.createElement('strong')
      subject.textContent = email.subject
      const meta = document.createElement('small')
      meta.textContent = `${email.to.join(', ')} · ${new Date(email._meta.created_at).toLocaleTimeString()}`
      button.append(subject, meta)
      button.addEventListener('click', () => show(email))
      li.append(button)
      return li
    }),
  )
}

async function load() {
  try {
    const page = await outbox.list({ orderBy: '-_meta.created_at', limit: 50 })
    $('error').hidden = true
    const changed = page.items.length !== emails.length || page.items[0]?.id !== emails[0]?.id
    emails = page.items
    if (changed) {
      // A new email arrived: show it (the newest), unless one is being read.
      if (selected === null || !emails.some((e) => e.id === selected)) show(emails[0])
      else render()
    }
  } catch (e) {
    $('error').hidden = false
    $('error').textContent = `Couldn't read ${realm}/${database}/outbox: ${e.message}. Does the realm use the email-capture function?`
  }
}

for (const button of document.querySelectorAll('.tabs button')) {
  button.addEventListener('click', () => {
    const html = button.dataset.tab === 'html'
    $('html').hidden = !html
    $('text').hidden = html
    for (const b of document.querySelectorAll('.tabs button')) b.setAttribute('aria-pressed', String(b === button))
  })
}
$('refresh').addEventListener('click', load)
await load()
setInterval(load, 2000)
