/**
 * @typedef {import('./client.js').Client} Client
 * @typedef {import('./client.js').RequestOptions} RequestOptions
 */

/**
 * An organization, as the signed-in user sees it.
 * @typedef {object} Org
 * @property {string} id
 * @property {string} name
 * @property {string | null} slug
 * @property {'active' | 'orphaned' | 'deleted'} status
 * @property {boolean} require_mfa  The organization requires a second factor of its members.
 * @property {boolean} mfa_met      False when it does and this session used none: ask for one (a
 *   session without it gets `OrgRequiresMFAError` from the organization's routes).
 * @property {Record<string, unknown>} claims  Written by administrators and functions; read-only here.
 * @property {string} created_at
 * @property {string[]} roles                  The user's effective role names there.
 * @property {Array<{ role: string, origin: string[] }>} effective_roles  Where each came from: `direct`, `default` or a team's name.
 * @property {string[]} teams                  The user's teams (ids).
 * @property {Record<string, string[]>} team_roles  The team roles that count now, by team id.
 */

/**
 * A team role held by a member: a role within one team, which doesn't need membership of it.
 * @typedef {object} OrgTeamRoleGrant
 * @property {string} team
 * @property {string} role
 * @property {string} [from]        The grant counts from then (always, when absent).
 * @property {string} [until]       ...and until then (for ever, when absent).
 * @property {string} [granted_by]  A user id; absent for administrators and functions.
 * @property {string} granted_at
 * @property {boolean} active       Whether it counts now.
 */

/**
 * A member of an organization.
 * @typedef {object} OrgMember
 * @property {string} user
 * @property {string} [email]        Per the realm's `members_visible_to`.
 * @property {string[]} roles        Direct roles.
 * @property {string[]} teams
 * @property {OrgTeamRoleGrant[]} team_roles  Team roles held, in force or not.
 * @property {Array<{ role: string, origin: string[] }>} effective_roles
 * @property {string[]} overlap      Roles held directly and also through a team or by default.
 * @property {boolean} [mfa]         Whether they have a second factor; only for managers.
 * @property {string[]} [mfa_roles]  The roles they hold now that require one; only for managers.
 * @property {string} joined_at
 * @property {string} [invited_by]
 */

/**
 * A team role to give, change or take away: the member and the role, and the window it counts
 * in (`from` and `until`, RFC 3339; absent is open).
 * @typedef {object} TeamRoleInput
 * @property {string} user   A member of the organization (they need not belong to the team).
 * @property {string} role   A team role the realm declares.
 * @property {string} [from]
 * @property {string} [until]
 */

/**
 * A member or role holder of one team.
 * @typedef {OrgMember & { in_team: boolean, roles_in_team: string[] }} OrgTeamMember
 */

/**
 * @typedef {object} OrgTeam
 * @property {string} id
 * @property {string} name
 * @property {string[]} roles
 * @property {string} created_at
 */

/**
 * The three parts of an email an organization wrote itself, in the logic-less syntax.
 * @typedef {object} OrgTemplateText
 * @property {string} subject
 * @property {string} text
 * @property {string} html
 */

/**
 * An organization's version of a kind of email in a language.
 * @typedef {object} OrgTemplate
 * @property {string} kind
 * @property {string} locale
 * @property {number} version
 * @property {string} updated_at
 * @property {string} [updated_by]
 * @property {string} [subject]   Only when one template is read.
 * @property {string} [text]
 * @property {string} [html]
 * @property {Array<{ version: number, at: string, by?: string } & Partial<OrgTemplateText>>} versions  Previous ones, newest first (at most 20).
 * @property {string[]} [removed]  Only in the answer to a save: what the HTML sanitizer drops from the sample.
 */

/**
 * @typedef {object} OrgInvitation
 * @property {string} id
 * @property {'user' | 'email' | 'none'} binding
 * @property {'pending' | 'used' | 'revoked' | 'expired'} status
 * @property {string} [user]
 * @property {string} [email]
 * @property {string[]} roles
 * @property {string[]} teams
 * @property {number} max_uses
 * @property {number} uses
 * @property {string} expires_at
 */

/**
 * What creating an invitation answers: the token and the link are shown only now.
 * @typedef {OrgInvitation & { token: string, link: string | null }} NewOrgInvitation
 */

/**
 * @typedef {object} InviteInput
 * @property {'user' | 'email' | 'none'} binding   Who may accept: one user, whoever holds a verified address, or anyone signed in with the link.
 * @property {string} [user]       A user id, for `user`.
 * @property {string} [email]      For `email`.
 * @property {string[]} [roles]
 * @property {string[]} [teams]    Team ids.
 * @property {boolean} [send]      Also email it (only `email`).
 * @property {string} [expiresIn]  A duration such as `2d` (default `7d`, at most `30d`).
 * @property {number} [maxUses]    For `none`; default 1.
 */

/**
 * An organization's sign-in connection: its own Google Workspace or Microsoft Entra tenant as a
 * provider for its people. The client secret is write-only: only whether it is set comes back.
 * @typedef {object} OrgConnection
 * @property {'google' | 'microsoft'} provider
 * @property {string} client_id
 * @property {string} [tenant]         Microsoft: the organization's tenant id.
 * @property {string} [hosted_domain]  Google: the Workspace domain.
 * @property {string[]} email_domains  When set, the verified address must also be in one of them.
 * @property {string[]} native_client_ids  Extra client ids the id-token sign-in accepts (mobile apps).
 * @property {string[]} join_roles     Organization roles given to people who join through it.
 * @property {boolean} enabled
 * @property {boolean} client_secret_set  Without a secret the connection is unavailable.
 * @property {string} [redirect_uri]   The address to register with the provider (when backd knows its public address).
 * @property {string} redirect_path    The same, without backd's public address.
 * @property {string} created_at
 * @property {string} updated_at
 */

/**
 * What the managers of an organization set for a connection (`manage: sso`).
 * @typedef {object} OrgConnectionInput
 * @property {string} clientId
 * @property {string} [clientSecret]    Required the first time; left out, the stored one is kept.
 * @property {string} [tenant]          Microsoft: required, a tenant id (never `common`, `organizations` or `consumers`).
 * @property {string} [hostedDomain]    Google: required, the Workspace domain.
 * @property {string[]} [emailDomains]
 * @property {string[]} [nativeClientIds]
 * @property {string[]} [joinRoles]     Roles given on joining; nothing the setting manager could not give.
 * @property {boolean} [enabled]        True for a new connection; unchanged on a replace.
 */

/**
 * The connections of an organization with the realm's limits.
 * @typedef {object} OrgConnections
 * @property {OrgConnection[]} items
 * @property {string[]} [providers]       The providers the realm lets organizations connect.
 * @property {number} [max_connections]
 */

/**
 * The body of a connection call, from the camelCase input.
 * @param {OrgConnectionInput} input
 * @returns {Record<string, unknown>}
 */
export function connectionBody(input) {
  /** @type {Record<string, unknown>} */
  const body = { client_id: input.clientId }
  if (input.clientSecret !== undefined) body.client_secret = input.clientSecret
  if (input.tenant !== undefined) body.tenant = input.tenant
  if (input.hostedDomain !== undefined) body.hosted_domain = input.hostedDomain
  if (input.emailDomains !== undefined) body.email_domains = input.emailDomains
  if (input.nativeClientIds !== undefined) body.native_client_ids = input.nativeClientIds
  if (input.joinRoles !== undefined) body.join_roles = input.joinRoles
  if (input.enabled !== undefined) body.enabled = input.enabled
  return body
}

/**
 * Organizations of the signed-in user: `client.orgs`. Members, teams and invitations hang off
 * one organization: `client.orgs.members(id)`, `client.orgs.teams(id)`, `client.orgs.invitations(id)`.
 * Documents in an organization are created and listed with `collection.inOrg(id)`.
 */
export class Orgs {
  /** @param {Client} client */
  constructor(client) {
    /** @internal */
    this.client = client
    /**
     * Invitations of an organization: `client.orgs.invitations(id).create(...)`. The function
     * also holds `preview(token)` and `accept(token)` for the person who got a link.
     */
    this.invitations = Object.assign(
      /** @param {string} org */
      (org) => new OrgInvitations(this.client, org),
      {
        /**
         * What a token offers (the organization, roles and teams), without taking it: "Join Acme as
         * editor?". Needs a session. An unknown, expired, revoked or used-up token is
         * `invalid_invitation`; a token bound to someone else is `invitation_not_for_you`.
         * @param {string} token
         * @param {RequestOptions} [opts]
         * @returns {Promise<{ org: { id: string, name: string }, roles: string[], teams: Array<{ id: string, name: string }>, binding: string, expires_at: string }>}
         */
        preview: async (token, opts) =>
          (await this.client.request({ method: 'POST', path: ['_orgs', 'invitations', 'preview'], body: { token }, ...opts })).data,
        /**
         * Takes an invitation: the user becomes a member, or gains its roles and teams.
         * @param {string} token
         * @param {RequestOptions} [opts]
         * @returns {Promise<Org>}
         */
        accept: async (token, opts) =>
          (await this.client.request({ method: 'POST', path: ['_orgs', 'invitations', 'accept'], body: { token }, ...opts })).data,
      },
    )
  }

  /**
   * Creates an organization; the caller holds the realm's `creator_roles` in it.
   * @param {{ name: string, slug?: string }} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<Org>}
   */
  async create(input, opts) {
    return (await this.client.request({ method: 'POST', path: ['_orgs'], body: input, ...opts })).data
  }

  /**
   * The organizations the user belongs to, with their roles there and whether this session
   * meets each one's second-factor requirement (`mfa_met`).
   * @param {RequestOptions} [opts]
   * @returns {Promise<Org[]>}
   */
  async list(opts) {
    return (await this.client.request({ method: 'GET', path: ['_orgs'], ...opts })).data.items
  }

  /**
   * One organization. `ForbiddenError` (`not_org_member`) for a non-member and an unknown
   * organization alike.
   * @param {string} id
   * @param {RequestOptions} [opts]
   * @returns {Promise<Org>}
   */
  async get(id, opts) {
    return (await this.client.request({ method: 'GET', path: ['_orgs', id], ...opts })).data
  }

  /**
   * Renames an organization, changes its slug, or sets `require_mfa` (which asks for a fresh proof
   * of identity: see `client.auth.withProof`).
   * @param {string} id
   * @param {{ name?: string, slug?: string, require_mfa?: boolean }} patch
   * @param {RequestOptions} [opts]
   * @returns {Promise<Org>}
   */
  async update(id, patch, opts) {
    return (await this.client.request({ method: 'PATCH', path: ['_orgs', id], body: patch, reauth: patch.require_mfa !== undefined, ...opts })).data
  }

  /**
   * Leaves an organization (allowed without a second factor; `last_org_manager` for the last
   * manager).
   * @param {string} id
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async leave(id, opts) {
    await this.client.request({ method: 'POST', path: ['_orgs', id, 'leave'], ...opts })
  }

  /**
   * Deletes an organization: access ends at once and a job cleans up its documents. Needs
   * `manage: org` and a fresh proof of identity: wrap the call in `client.auth.withProof`.
   * @param {string} id
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async delete(id, opts) {
    await this.client.request({ method: 'DELETE', path: ['_orgs', id], reauth: true, ...opts })
  }

  /**
   * The members of an organization: `list`, `update` (direct roles) and `remove`.
   * @param {string} org
   * @returns {OrgMembers}
   */
  members(org) {
    return new OrgMembers(this.client, org)
  }

  /**
   * The organization's own versions of the realm's emails (`email.customizable`): `list`, `get`,
   * `put`, `delete`, `preview` and `test`.
   * @param {string} org
   * @returns {OrgTemplates}
   */
  templates(org) {
    return new OrgTemplates(this.client, org)
  }

  /**
   * The teams of an organization: groups of members that carry roles.
   * @param {string} org
   * @returns {OrgTeams}
   */
  teams(org) {
    return new OrgTeams(this.client, org)
  }

  /**
   * The organization's own sign-in connections (`organizations.sso`): `list`, `get`, `set` and
   * `delete`, for holders of `manage: sso`. People then sign in through them with
   * `client.auth.signInWith(provider, { org })`.
   * @param {string} org
   * @returns {OrgSso}
   */
  sso(org) {
    return new OrgSso(this.client, org)
  }
}

/** The sign-in connections of one organization. */
export class OrgSso {
  /**
   * @param {Client} client
   * @param {string} org
   */
  constructor(client, org) {
    /** @internal */
    this.client = client
    /** @internal */
    this.org = org
  }

  /**
   * The connections, with the providers and the number of connections the realm allows. A realm
   * with organization sign-in off answers `NotFoundError`.
   * @param {RequestOptions} [opts]
   * @returns {Promise<OrgConnections>}
   */
  async list(opts) {
    return (await this.client.request({ method: 'GET', path: ['_orgs', this.org, 'sso'], ...opts })).data
  }

  /**
   * One connection; a `NotFoundError` when the organization has none with that provider.
   * @param {'google' | 'microsoft'} provider
   * @param {RequestOptions} [opts]
   * @returns {Promise<OrgConnection>}
   */
  async get(provider, opts) {
    return (await this.client.request({ method: 'GET', path: ['_orgs', this.org, 'sso', provider], ...opts })).data
  }

  /**
   * Creates the connection or replaces every field of it. The client secret goes in and never
   * comes out. Needs a fresh proof of identity: wrap the call in `client.auth.withProof`.
   * @param {'google' | 'microsoft'} provider
   * @param {OrgConnectionInput} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<OrgConnection>}
   */
  async set(provider, input, opts) {
    return (await this.client.request({ method: 'PUT', path: ['_orgs', this.org, 'sso', provider], body: connectionBody(input), reauth: true, ...opts })).data
  }

  /**
   * Removes the connection and its client secret. Needs a fresh proof of identity.
   * @param {'google' | 'microsoft'} provider
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async delete(provider, opts) {
    await this.client.request({ method: 'DELETE', path: ['_orgs', this.org, 'sso', provider], reauth: true, ...opts })
  }
}

/** The members of one organization. */
export class OrgMembers {
  /**
   * @param {Client} client
   * @param {string} org
   */
  constructor(client, org) {
    /** @internal */
    this.client = client
    /** @internal */
    this.org = org
  }

  /**
   * A page of members sorted by user id: `{ items, has_more, next }`; pass `next` as `after`.
   * @param {{ limit?: number, after?: string }} [params]
   * @param {RequestOptions} [opts]
   * @returns {Promise<{ items: OrgMember[], has_more: boolean, next?: string }>}
   */
  async list(params = {}, opts) {
    return (await this.client.request({ method: 'GET', path: ['_orgs', this.org, 'members'], query: { limit: params.limit, after: params.after }, ...opts })).data
  }

  /**
   * Replaces a member's direct roles. With `manage: members` only roles that open nothing the
   * caller lacks; with `manage: roles` any.
   * @param {string} user
   * @param {string[]} roles
   * @param {RequestOptions} [opts]
   * @returns {Promise<OrgMember>}
   */
  async update(user, roles, opts) {
    return (await this.client.request({ method: 'PATCH', path: ['_orgs', this.org, 'members', user], body: { roles }, ...opts })).data
  }

  /**
   * Removes a member; their access ends at once.
   * @param {string} user
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async remove(user, opts) {
    await this.client.request({ method: 'DELETE', path: ['_orgs', this.org, 'members', user], ...opts })
  }
}

/** The teams of one organization. */
export class OrgTeams {
  /**
   * @param {Client} client
   * @param {string} org
   */
  constructor(client, org) {
    /** @internal */
    this.client = client
    /** @internal */
    this.org = org
  }

  /**
   * @param {RequestOptions} [opts]
   * @returns {Promise<OrgTeam[]>}
   */
  async list(opts) {
    return (await this.client.request({ method: 'GET', path: ['_orgs', this.org, 'teams'], ...opts })).data.items
  }

  /**
   * @param {{ name: string, roles?: string[] }} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<OrgTeam>}
   */
  async create(input, opts) {
    return (await this.client.request({ method: 'POST', path: ['_orgs', this.org, 'teams'], body: input, ...opts })).data
  }

  /**
   * @param {string} team
   * @param {{ name?: string, roles?: string[] }} patch
   * @param {RequestOptions} [opts]
   * @returns {Promise<OrgTeam>}
   */
  async update(team, patch, opts) {
    return (await this.client.request({ method: 'PATCH', path: ['_orgs', this.org, 'teams', team], body: patch, ...opts })).data
  }

  /**
   * Deletes a team: it leaves every membership, and its roles go with it.
   * @param {string} team
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async delete(team, opts) {
    await this.client.request({ method: 'DELETE', path: ['_orgs', this.org, 'teams', team], ...opts })
  }

  /**
   * The members of a team and the holders of its team roles, in force now. With `role`, only the
   * holders of that role (members of the organization who need not belong to the team). Emails
   * follow the realm's `members_visible_to`.
   * @param {string} team
   * @param {{ role?: string, limit?: number, after?: string }} [params]
   * @param {RequestOptions} [opts]
   * @returns {Promise<{ items: OrgTeamMember[], has_more: boolean, next?: string }>}
   */
  async members(team, params = {}, opts) {
    const query = { role: params.role, limit: params.limit, after: params.after }
    return (await this.client.request({ method: 'GET', path: ['_orgs', this.org, 'teams', team, 'members'], query, ...opts })).data
  }

  /**
   * Gives a member a team role in a team: with `manage: teams`, any; otherwise a holder of a team
   * role of that team whose `grants` lists it. `from` and `until` set the window it counts in.
   * `OrgRoleRequiresMFAError` when the role that lets the caller does and the session has no second factor.
   * @param {string} team
   * @param {TeamRoleInput} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<OrgTeamMember>}
   */
  async grantRole(team, input, opts) {
    return (await this.client.request({ method: 'POST', path: ['_orgs', this.org, 'teams', team, 'roles'], body: input, ...opts })).data
  }

  /**
   * Replaces the window of a team role the member holds (`NotFoundError` if they don't).
   * @param {string} team
   * @param {TeamRoleInput} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<OrgTeamMember>}
   */
  async setRoleWindow(team, input, opts) {
    return (await this.client.request({ method: 'PATCH', path: ['_orgs', this.org, 'teams', team, 'roles'], body: input, ...opts })).data
  }

  /**
   * Takes a team role from a member: access through it ends at their next request.
   * @param {string} team
   * @param {{ user: string, role: string }} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<OrgTeamMember>}
   */
  async revokeRole(team, input, opts) {
    return (await this.client.request({ method: 'DELETE', path: ['_orgs', this.org, 'teams', team, 'roles'], body: input, ...opts })).data
  }

  /**
   * Puts a member of the organization in a team; they gain its roles.
   * @param {string} team
   * @param {string} user
   * @param {RequestOptions} [opts]
   * @returns {Promise<OrgMember>}
   */
  async addMember(team, user, opts) {
    return (await this.client.request({ method: 'PUT', path: ['_orgs', this.org, 'teams', team, 'members', user], ...opts })).data
  }

  /**
   * Takes a member out of a team.
   * @param {string} team
   * @param {string} user
   * @param {RequestOptions} [opts]
   * @returns {Promise<OrgMember>}
   */
  async removeMember(team, user, opts) {
    return (await this.client.request({ method: 'DELETE', path: ['_orgs', this.org, 'teams', team, 'members', user], ...opts })).data
  }
}

/**
 * The email templates of one organization: its own text for the kinds the realm lets companies
 * customize, in the logic-less syntax (`{{Link}}`, `{{App.name}}`, `{{#App.logo}}…{{/App.logo}}`).
 * Needs the capability `email.overrides.permission` names (`manage: org` by default).
 */
export class OrgTemplates {
  /**
   * @param {Client} client
   * @param {string} org
   */
  constructor(client, org) {
    /** @internal */
    this.client = client
    /** @internal */
    this.org = org
  }

  /**
   * The kinds that can be customized, with the placeholders each must print, and the
   * organization's overrides (without their text).
   * @param {RequestOptions} [opts]
   * @returns {Promise<{ customizable: Record<string, { required: string[] }>, items: OrgTemplate[] }>}
   */
  async list(opts) {
    return (await this.client.request({ method: 'GET', path: ['_orgs', this.org, 'templates'], ...opts })).data
  }

  /**
   * One template with its versions; `NotFoundError` if the organization has none.
   * @param {string} kind
   * @param {string} locale
   * @param {RequestOptions} [opts]
   * @returns {Promise<OrgTemplate>}
   */
  async get(kind, locale, opts) {
    return (await this.client.request({ method: 'GET', path: ['_orgs', this.org, 'templates', kind, locale], ...opts })).data
  }

  /**
   * Saves a version. A template with a problem rejects with a `ValidationError` (`invalid_template`)
   * whose `details` say where (`text`: `line 3, column 5: …`).
   * @param {string} kind
   * @param {string} locale
   * @param {OrgTemplateText} template
   * @param {RequestOptions} [opts]
   * @returns {Promise<OrgTemplate>}
   */
  async put(kind, locale, template, opts) {
    return (await this.client.request({ method: 'PUT', path: ['_orgs', this.org, 'templates', kind, locale], body: template, ...opts })).data
  }

  /**
   * Goes back to the realm's own template of the kind.
   * @param {string} kind
   * @param {string} locale
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async delete(kind, locale, opts) {
    await this.client.request({ method: 'DELETE', path: ['_orgs', this.org, 'templates', kind, locale], ...opts })
  }

  /**
   * Renders a draft, or the saved version without one, without sending anything. `data: 'real'`
   * calls the realm's data hook. Show `html` in a sandboxed frame.
   * @param {string} kind
   * @param {string} locale
   * @param {{ draft?: OrgTemplateText, data?: 'sample' | 'real' }} [input]
   * @param {RequestOptions} [opts]
   * @returns {Promise<{ subject: string, text: string, html: string, removed: string[] }>}
   */
  async preview(kind, locale, input = {}, opts) {
    const body = { ...input.draft, data: input.data }
    return (await this.client.request({ method: 'POST', path: ['_orgs', this.org, 'templates', kind, locale, 'preview'], body, ...opts })).data
  }

  /**
   * Sends a draft (or the saved version) to your own address, marked [TEST]. At most 10 per hour
   * for the organization (`RetryableError`, 429).
   * @param {string} kind
   * @param {string} locale
   * @param {{ draft?: OrgTemplateText }} [input]
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async test(kind, locale, input = {}, opts) {
    await this.client.request({ method: 'POST', path: ['_orgs', this.org, 'templates', kind, locale, 'test'], body: { ...input.draft }, ...opts })
  }
}

/** The invitations of one organization. */
export class OrgInvitations {
  /**
   * @param {Client} client
   * @param {string} org
   */
  constructor(client, org) {
    /** @internal */
    this.client = client
    /** @internal */
    this.org = org
  }

  /**
   * Invites. The answer holds the `token` and the `link` (the realm's `invitation_link`), shown
   * only now: show them as a QR code, send them another way, or offer copy and paste; `send: true`
   * also emails an `email` binding.
   * @param {InviteInput} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<NewOrgInvitation>}
   */
  async create(input, opts) {
    const { expiresIn, maxUses, ...rest } = input
    /** @type {Record<string, unknown>} */
    const body = { ...rest }
    if (expiresIn !== undefined) body.expires_in = expiresIn
    if (maxUses !== undefined) body.max_uses = maxUses
    return (await this.client.request({ method: 'POST', path: ['_orgs', this.org, 'invitations'], body, ...opts })).data
  }

  /**
   * Status, binding, uses and expiry, newest first; never tokens.
   * @param {RequestOptions} [opts]
   * @returns {Promise<OrgInvitation[]>}
   */
  async list(opts) {
    return (await this.client.request({ method: 'GET', path: ['_orgs', this.org, 'invitations'], ...opts })).data.items
  }

  /**
   * @param {string} id
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async revoke(id, opts) {
    await this.client.request({ method: 'DELETE', path: ['_orgs', this.org, 'invitations', id], ...opts })
  }

  /**
   * A new token and link for a pending invitation; the old link stops working.
   * @param {string} id
   * @param {RequestOptions} [opts]
   * @returns {Promise<NewOrgInvitation>}
   */
  async newLink(id, opts) {
    return (await this.client.request({ method: 'POST', path: ['_orgs', this.org, 'invitations', id, 'link'], ...opts })).data
  }

  /**
   * Sends the email of an invitation bound to an address again.
   * @param {string} id
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async send(id, opts) {
    await this.client.request({ method: 'POST', path: ['_orgs', this.org, 'invitations', id, 'send'], ...opts })
  }
}
