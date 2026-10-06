// A function that makes a thumbnail of a person's photo and stores it in a field the
// collection's rules refuse to clients (`update: … && !('thumbnail' in changed())`), so
// only this function can write it: it uses ctx.admin.db (function.yaml: `admin: true`).
//
// Called by the app, or by a worker after an upload, with { "person": "<document id>" }.
// `resize` is what needs an image library; index.ts below gives it one.
export function makeThumbnailer(resize) {
  return async (ctx) => {
    const { person } = ctx.input ?? {}
    if (typeof person !== 'string') throw ctx.error(400, 'person_required', 'say which person: { "person": "<id>" }')

    const people = ctx.admin.db('main').collection('people')
    const photo = await people.files(person, 'photo').get() // the original; NotFoundError when there is none
    const small = await resize(await photo.bytes(), 128)

    // A single field: the new thumbnail replaces the old one, whose object backd then deletes.
    const doc = await people.files(person, 'thumbnail').put(small, { name: 'thumbnail.png', type: 'image/png' })
    return { thumbnail: doc.thumbnail.id, size: doc.thumbnail.size }
  }
}
