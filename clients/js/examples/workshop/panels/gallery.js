// Panel 7: a public gallery (docs: Files -> Public fields).
//
// The gallery's pictures are meant for everyone, so the `photo` field of `gallery` is public:
// its files live in the realm's public bucket and each document carries the permanent address
// the picture is read at. Anyone may list the gallery (no sign-in), and the page puts the address
// in an <img>: no signed link, no request to backd for the bytes. Only staff may add pictures.
import { publicUrl } from 'backd-js'

/** @param {{ db: any }} deps */
export function galleryPanel({ db }) {
  const gallery = db.collection('gallery')
  return {
    pictures: [],
    caption: '',

    loadGallery() {
      return this.step('List the gallery', async () => {
        this.pictures = (await gallery.list({ orderBy: '-_meta.created_at', limit: 24 })).items
      })
    },

    // A picture's address, read from the document: the thumbnail when it is ready, else the original.
    pictureUrl(picture) {
      return publicUrl(picture.photo, 'thumb') ?? publicUrl(picture.photo)
    },

    addPicture(event) {
      const input = event.target
      const file = input.files?.[0]
      input.value = ''
      if (!file) return undefined
      return this.step(`Add ${file.name} to the gallery`, async () => {
        const doc = await gallery.create({ title: this.caption.trim() || file.name })
        await gallery.uploadFile(doc.id, 'photo', file)
        this.caption = ''
        this.pictures = (await gallery.list({ orderBy: '-_meta.created_at', limit: 24 })).items
      })
    },

    removePicture(picture) {
      return this.step(`Remove ${picture.title}`, async () => {
        await gallery.delete(picture.id)
        this.pictures = this.pictures.filter((p) => p.id !== picture.id)
      })
    },
  }
}
