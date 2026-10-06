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
