// The adminui example realm (examples/config/adminui/realm.yaml): one user
// per admin level, plus one with no admin role at all.
export const REALM = 'adminui'
export const PASSWORD = 'dev-p4ssw0rd!'

export const users = {
  admin: 'admin@adminui.example',
  viewer: 'viewer@adminui.example',
  support: 'support@adminui.example',
  plain: 'plain@adminui.example',
} as const
