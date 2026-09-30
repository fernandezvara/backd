export { createClient, Client } from './client.js'
export { Auth } from './auth.js'
export { Database, Collection, ifMatchValue } from './data.js'
export { Admin } from './admin.js'
export { Job, JobTimeoutError } from './functions.js'
export {
  BackdError,
  ValidationError,
  AuthenticationError,
  ForbiddenError,
  NotFoundError,
  ConflictError,
  VersionMismatchError,
  RetryableError,
  NetworkError,
} from './errors.js'
export { memoryStorage, localStorageStorage } from './storage.js'

/**
 * @typedef {import('./client.js').ClientOptions} ClientOptions
 * @typedef {import('./client.js').RequestOptions} RequestOptions
 * @typedef {import('./client.js').RetryOptions} RetryOptions
 * @typedef {import('./storage.js').TokenStorage} TokenStorage
 * @typedef {import('./auth.js').User} User
 * @typedef {import('./auth.js').Session} Session
 * @typedef {import('./auth.js').SessionInfo} SessionInfo
 * @typedef {import('./auth.js').AuthEvent} AuthEvent
 * @typedef {import('./errors.js').ErrorDetail} ErrorDetail
 * @typedef {import('./data.js').Meta} Meta
 * @typedef {import('./data.js').ListParams} ListParams
 * @typedef {import('./data.js').WriteOptions} WriteOptions
 * @typedef {import('./admin.js').AdminUser} AdminUser
 * @typedef {import('./admin.js').UserPage} UserPage
 * @typedef {import('./admin.js').Invitation} Invitation
 * @typedef {import('./admin.js').NewInvitation} NewInvitation
 * @typedef {import('./functions.js').JobData} JobData
 * @typedef {import('./functions.js').JobResult} JobResult
 */

/**
 * @template {object} [T=Record<string, any>]
 * @typedef {import('./data.js').Doc<T>} Doc
 */

/**
 * @template {object} [T=Record<string, any>]
 * @typedef {import('./data.js').Page<T>} Page
 */
