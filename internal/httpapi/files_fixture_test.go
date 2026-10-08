package httpapi

// What the tests that use file fields share: a realm with a storage (pointed at a
// fake S3 server by each test) and collections that declare files.

const filesStorage = `storage:
  provider: minio
  endpoint: http://127.0.0.1:1
  bucket: files
  prefix: t
  access_key: secret:STORAGE_ACCESS_KEY
  secret_key: secret:STORAGE_SECRET_KEY
`

const filesSchema = `{
  "type": "object",
  "properties": {"title": {"type": "string"}, "published": {"type": "boolean"}},
  "required": ["title"]
}`

const filesConfig = `files:
  avatar:
    max_size: 4KiB
    types: [image/png, image/jpeg, image/webp]
  receipts:
    multiple: true
    max_files: 2
    max_size: 8KiB
    types: [application/pdf, image/*]
  thumbnail:
    max_size: 4KiB
    types: [image/png]
  manual:
    max_size: 8KiB
    download: proxy
    cache: 1h
  # Pictures served through backd, with a version.
  proxied:
    max_size: 64KiB
    types: [image/*]
    download: proxy
    cache: 1h
    versions:
      thumb: {max_width: 10, max_height: 10, format: png}
  # A picture with versions: workers make thumb, functions may redo big and make mark.
  picture:
    max_size: 64KiB
    max_pixels: 1000
    types: [image/*]
    versions:
      thumb: {max_width: 10, max_height: 10, fit: cover, format: jpeg, quality: 70}
      big: {max_width: 100, writable: true}
      mark: {writable: true}
`

// A document's owner reads and changes it; nobody but a function (ctx.admin.db,
// which skips rules) writes the thumbnail.
const filesRules = `
read: user != nil && document._meta.owner == user.id
create: user != nil && data.thumbnail == nil
update: >
  user != nil && document._meta.owner == user.id
  && !('thumbnail' in changed())
delete: user != nil && document._meta.owner == user.id
`

// forms takes anonymous submissions with a required scan: a collection for creating
// documents together with their files.
const formsSchema = `{
  "type": "object",
  "properties": {"title": {"type": "string"}},
  "required": ["title", "scan"]
}`

const formsConfig = `files:
  scan:
    max_size: 4KiB
    types: [image/png]
  extras:
    multiple: true
    max_files: 2
    max_size: 4KiB
    versions:
      thumb: {max_width: 8}
`

const formsRules = `
read: user != nil && document._meta.owner == user.id
create: "true"
update: user != nil && document._meta.owner == user.id
`

// videos takes direct uploads, to an existing document and before it exists.
const videosSchema = `{
  "type": "object",
  "properties": {"title": {"type": "string"}},
  "required": ["title", "clip"]
}`

const videosConfig = `files:
  clip:
    upload: direct
    max_size: 4KiB
    types: [image/png]
  clips:
    upload: direct
    multiple: true
    max_files: 2
    max_size: 4KiB
  photo:
    max_size: 4KiB
  still:
    upload: direct
    max_size: 64KiB
    versions:
      thumb: {max_width: 8}
`

const videosRules = `
read: user != nil && document._meta.owner == user.id
create: "true"
update: user != nil && document._meta.owner == user.id
`

// vault deletes what a user owns when they are erased; profiles anonymizes, removing
// the avatar and leaving the receipts; library (no policy) keeps everything.
const vaultConfig = filesConfig + `on_owner_delete:
  action: delete
`

const profilesConfig = filesConfig + `on_owner_delete:
  action: anonymize
  remove: [avatar]
`
