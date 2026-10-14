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
 */

/**
 * A member of an organization.
 * @typedef {object} OrgMember
 * @property {string} user
 * @property {string} [email]        Per the realm's `members_visible_to`.
 * @property {string[]} roles        Direct roles.
 * @property {string[]} teams
 * @property {Array<{ role: string, origin: string[] }>} effective_roles
 * @property {string[]} overlap      Roles held directly and also through a team or by default.
 * @property {boolean} [mfa]         Whether they have a second factor; only for managers.
 * @property {string} joined_at
 * @property {string} [invited_by]
 */

/**
 * @typedef {object} OrgTeam
 * @property {string} id
 * @property {string} name
 * @property {string[]} roles
 * @property {string} created_at
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
   * The teams of an organization: groups of members that carry roles.
   * @param {string} org
   * @returns {OrgTeams}
   */
  teams(org) {
    return new OrgTeams(this.client, org)
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
