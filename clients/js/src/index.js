export { createClient, Client } from './client.js'
export { Auth } from './auth.js'
export { Database, Collection, ifMatchValue } from './data.js'
export { Admin } from './admin.js'
export { Push } from './push.js'
export { Consents } from './consents.js'
export { Account } from './account.js'
export { Config } from './config.js'
export { Orgs, OrgMembers, OrgTeams, OrgTemplates, OrgInvitations } from './orgs.js'
export { Files, publicUrl, versionStatus } from './files.js'
export { expand } from './expand.js'
export { fillBuckets } from './buckets.js'
export { Sha256, sha256Blob } from './sha256.js'
export { Job, JobTimeoutError } from './functions.js'
export {
  BackdError,
  ValidationError,
  AuthenticationError,
  PasswordExpiredError,
  ForbiddenError,
  NotFoundError,
  ConflictError,
  IdTakenError,
  InvalidIdError,
  OrgRequiresMFAError,
  OrgRoleRequiresMFAError,
  NotTeamMemberError,
  InvalidExpandError,
  FieldEncryptedError,
  ExpandTooLargeError,
  InvalidAggregateError,
  AggregateNotEnabledError,
  AggregateTooExpensiveError,
  SearchNotEnabledError,
  SearchTooExpensiveError,
  IndexBuildingError,
  TeamError,
  NotOrgMemberError,
  NotAllowedError,
  SsoRequiredError,
  SsoSessionError,
  LastOrgManagerError,
  VersionMismatchError,
  RetryableError,
  NetworkError,
  VerificationRequiredError,
  SecondStepRequiredError,
  ConsentRequiredError,
  RealtimeNotEnabledError,
  TooManySubscriptionsError,
  InvalidResumeTokenError,
  SessionEndedError,
} from './errors.js'
export { Realtime, LiveView } from './realtime.js'
export { memoryStorage, localStorageStorage } from './storage.js'
export { parseReturn as parseOAuthReturn } from './oauth.js'

/**
 * @typedef {import('./client.js').ClientOptions} ClientOptions
 * @typedef {import('./typed.js').RealmShape} RealmShape
 * @typedef {import('./client.js').RequestOptions} RequestOptions
 * @typedef {import('./client.js').RetryOptions} RetryOptions
 * @typedef {import('./storage.js').TokenStorage} TokenStorage
 * @typedef {import('./auth.js').User} User
 * @typedef {import('./auth.js').Session} Session
 * @typedef {import('./auth.js').Me} Me
 * @typedef {import('./auth.js').Identity} Identity
 * @typedef {import('./auth.js').Profile} Profile
 * @typedef {import('./auth.js').ProviderSession} ProviderSession
 * @typedef {import('./auth.js').SignInWithOptions} SignInWithOptions
 * @typedef {import('./oauth.js').OAuthStorage} OAuthStorage
 * @typedef {import('./oauth.js').OAuthReturn} OAuthReturn
 * @typedef {import('./auth.js').SessionInfo} SessionInfo
 * @typedef {import('./auth.js').AuthEvent} AuthEvent
 * @typedef {import('./auth.js').ConsentChallenge} ConsentChallenge
 * @typedef {import('./consents.js').ConsentInfo} ConsentInfo
 * @typedef {import('./consents.js').ConsentText} ConsentText
 * @typedef {import('./consents.js').ConsentState} ConsentState
 * @typedef {import('./push.js').Device} Device
 * @typedef {import('./push.js').DeviceInput} DeviceInput
 * @typedef {import('./push.js').PermissionState} PermissionState
 * @typedef {import('./errors.js').ErrorDetail} ErrorDetail
 * @typedef {import('./realtime.js').SubscriptionSpec} SubscriptionSpec
 * @typedef {import('./realtime.js').LiveState} LiveState
 * @typedef {import('./realtime.js').LiveStatus} LiveStatus
 * @typedef {import('./realtime.js').SubscribeOptions} SubscribeOptions
 * @typedef {import('./data.js').Meta} Meta
 * @typedef {import('./data.js').ListParams} ListParams
 * @typedef {import('./data.js').WriteOptions} WriteOptions
 * @typedef {import('./data.js').GetOptions} GetOptions
 * @typedef {import('./data.js').Expanded} Expanded
 * @typedef {import('./expand.js').ExpandPart} ExpandPart
 * @typedef {import('./data.js').AggregateParams} AggregateParams
 * @typedef {import('./data.js').AggregateGroup} AggregateGroup
 * @typedef {import('./data.js').AggregateResult} AggregateResult
 * @typedef {import('./buckets.js').BucketUnit} BucketUnit
 * @typedef {import('./data.js').DeleteOptions} DeleteOptions
 * @typedef {import('./data.js').CreateOptions} CreateOptions
 * @typedef {import('./admin.js').ConfigCheck} ConfigCheck
 * @typedef {import('./admin.js').StorageStatus} StorageStatus
 * @typedef {import('./admin.js').StorageReconcile} StorageReconcile
 * @typedef {import('./files.js').FileDetails} FileDetails
 * @typedef {import('./files.js').FileContent} FileContent
 * @typedef {import('./files.js').VersionState} VersionState
 * @typedef {import('./files.js').VersionOptions} VersionOptions
 * @typedef {import('./files.js').VersionParams} VersionParams
 * @typedef {import('./files.js').FileVersion} FileVersion
 * @typedef {import('./files.js').FileLink} FileLink
 * @typedef {import('./files.js').PutOptions} PutOptions
 * @typedef {import('./files.js').UploadOptions} UploadOptions
 * @typedef {import('./files.js').FileField} FileField
 * @typedef {import('./files.js').PreparedUpload} PreparedUpload
 * @typedef {import('./client.js').UploadProgress} UploadProgress
 * @typedef {import('./admin.js').AdminUser} AdminUser
 * @typedef {import('./admin.js').AdminAccess} AdminAccess
 * @typedef {import('./admin.js').AdminConfig} AdminConfig
 * @typedef {import('./admin.js').UserSession} UserSession
 * @typedef {import('./admin.js').PushSend} PushSend
 * @typedef {import('./admin.js').PushStatus} PushStatus
 * @typedef {import('./admin.js').UserPage} UserPage
 * @typedef {import('./admin.js').PasswordState} PasswordState
 * @typedef {import('./admin.js').PasswordsExpired} PasswordsExpired
 * @typedef {import('./admin.js').Invitation} Invitation
 * @typedef {import('./admin.js').NewInvitation} NewInvitation
 * @typedef {import('./admin.js').SentInvitation} SentInvitation
 * @typedef {import('./admin.js').AdminOrg} AdminOrg
 * @typedef {import('./admin.js').OrgOwnedReport} OrgOwnedReport
 * @typedef {import('./orgs.js').Org} Org
 * @typedef {import('./orgs.js').OrgMember} OrgMember
 * @typedef {import('./orgs.js').OrgTeam} OrgTeam
 * @typedef {import('./orgs.js').OrgTeamMember} OrgTeamMember
 * @typedef {import('./orgs.js').OrgTeamRoleGrant} OrgTeamRoleGrant
 * @typedef {import('./orgs.js').TeamRoleInput} TeamRoleInput
 * @typedef {import('./orgs.js').OrgTemplate} OrgTemplate
 * @typedef {import('./orgs.js').OrgTemplateText} OrgTemplateText
 * @typedef {import('./orgs.js').OrgInvitation} OrgInvitation
 * @typedef {import('./orgs.js').NewOrgInvitation} NewOrgInvitation
 * @typedef {import('./orgs.js').InviteInput} OrgInviteInput
 * @typedef {import('./orgs.js').OrgConnection} OrgConnection
 * @typedef {import('./orgs.js').OrgConnectionInput} OrgConnectionInput
 * @typedef {import('./orgs.js').OrgConnections} OrgConnections
 * @typedef {import('./admin.js').OwnedReport} OwnedReport
 * @typedef {import('./admin.js').APIKeyInfo} APIKeyInfo
 * @typedef {import('./admin.js').NewAPIKey} NewAPIKey
 * @typedef {import('./admin.js').SecretInfo} SecretInfo
 * @typedef {import('./admin.js').AuditRecord} AuditRecord
 * @typedef {import('./admin.js').AuditPage} AuditPage
 * @typedef {import('./admin.js').InvocationRecord} InvocationRecord
 * @typedef {import('./admin.js').InvocationsPage} InvocationsPage
 * @typedef {import('./admin.js').JobSummary} JobSummary
 * @typedef {import('./admin.js').JobsPage} JobsPage
 * @typedef {import('./admin.js').Schedule} Schedule
 * @typedef {import('./admin.js').CheckReport} CheckReport
 * @typedef {import('./admin.js').StorageCheck} StorageCheck
 * @typedef {import('./admin.js').CheckReportSummary} CheckReportSummary
 * @typedef {import('./admin.js').DataCheckStarted} DataCheckStarted
 * @typedef {import('./admin.js').DataChecksList} DataChecksList
 * @typedef {import('./functions.js').JobData} JobData
 * @typedef {import('./functions.js').Step} Step
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

/**
 * A client typed from a realm's generated types: `TypedClient<Realm>` (see `createClient`).
 * @template {import('./typed.js').RealmShape} R
 * @typedef {import('./typed.js').TypedClient<R>} TypedClient
 */
