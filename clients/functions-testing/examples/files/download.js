// A download function: counts the download, applies its own check, and returns the link
// the app opens (a function is called with POST and answers JSON, so it can't stream the
// file itself). It runs as the caller (ctx.db): the document's `read` rule decides whether
// they may have the link at all, exactly as for GET …/_files/{field}/{id}?link=json.
//
// Called with { "release": "<document id>", "file": "<file id>" }.
export default async function download(ctx) {
  const { release, file } = ctx.input ?? {}
  if (typeof release !== 'string' || typeof file !== 'string') throw ctx.error(400, 'bad_input', 'give { "release", "file" }')

  const releases = ctx.db('main').collection('releases')
  const link = await releases.files(release, 'assets').link(file) // NotFoundError if they may not read it

  // The count is the function's to keep: it is written with ctx.admin.db, in a field
  // the rules don't let a client touch.
  const counters = ctx.admin.db('main').collection('releases')
  const doc = await counters.get(release)
  await counters.patch(release, { downloads: (doc.downloads ?? 0) + 1 }, { ifMatch: doc._meta.version })

  return { url: link.url, expires_at: link.expires_at }
}
