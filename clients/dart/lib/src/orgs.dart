import 'dart:convert';

import 'auth.dart';
import 'client.dart';
import 'storage.dart';

/// Organizations of the signed-in user: `client.orgs`. Members, teams and
/// invitations hang off one organization: `client.orgs.members(id)`,
/// `client.orgs.teams(id)`, `client.orgs.invitations(id)`. Documents in an
/// organization are created and listed with `collection.inOrg(id)`.
final class Orgs {
  Orgs(this.client);

  final BackdClient<Object?> client;

  /// Creates an organization; the caller holds the realm's `creator_roles`
  /// in it. The answer also holds `session`, a new session scoped to it:
  /// the client keeps it, so `client.org(id)` is signed in.
  Future<Map<String, Object?>> create(Map<String, Object?> input,
      {RequestOptions? options}) async {
    final res = await client.request(RequestInit(
      method: 'POST',
      path: const ['_orgs'],
      body: input,
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    final data = Map<String, Object?>.from(res.data as Map);
    final made = data['session'];
    if (made is Map && made['token'] is String && client.keepsOrgSessions) {
      await client.org('${data['id']}').auth.signedInWith(Session(
            token: made['token'] as String,
            sessionId: '',
            expiresAt: '${made['expires_at'] ?? ''}',
            user: null,
            org: made['org'] as String?,
          ));
    }
    return data;
  }

  /// The organizations this app holds a session for, as clients made with
  /// `client.org`: one entry for each, with the id the sign-in answered
  /// (when it did), when the session ends and, once `auth.org()` has been
  /// asked, its name. It is what an organization switcher lists; the API
  /// never lists the organizations a person is in.
  Future<List<({String ref, String? id, String? expiresAt, String? name})>>
      signedIn() async {
    final storage = client.root.storage;
    final names = await storage.slots();
    final out = <({String ref, String? id, String? expiresAt, String? name})>[];
    for (final ref in names.where((n) => !n.startsWith(orgNotePrefix))) {
      Map<String, Object?> meta = const {};
      try {
        final text = await storage.slot('$orgNotePrefix$ref')?.get();
        if (text != null) {
          meta = Map<String, Object?>.from(jsonDecode(text) as Map);
        }
      } on Object {
        // A damaged note is as if there was none.
      }
      out.add((
        ref: ref,
        id: meta['org'] as String?,
        expiresAt: meta['expires_at'] as String?,
        name: meta['name'] as String?,
      ));
    }
    return out;
  }

  /// The organizations the user belongs to, with their roles there and
  /// whether this session meets each one's second-factor requirement
  /// (`mfa_met`).
  Future<List<Map<String, Object?>>> list({RequestOptions? options}) async {
    final res = await client.request(RequestInit(
      method: 'GET',
      path: const ['_orgs'],
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return [
      for (final o in (res.data as Map?)?['items'] as List? ?? const [])
        if (o is Map) Map<String, Object?>.from(o),
    ];
  }

  /// One organization. `ForbiddenError` (`not_org_member`) for a non-member
  /// and an unknown organization alike.
  Future<Map<String, Object?>> get(String id,
      {RequestOptions? options}) async {
    final res = await client.request(RequestInit(
      method: 'GET',
      path: ['_orgs', id],
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }

  /// Renames an organization, changes its slug, or sets `require_mfa`
  /// (which asks for a fresh proof of identity: see `client.auth.reauth`).
  Future<Map<String, Object?>> update(
    String id, {
    String? name,
    String? slug,
    bool? requireMfa,
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'PATCH',
      path: ['_orgs', id],
      body: {
        if (name != null) 'name': name,
        if (slug != null) 'slug': slug,
        if (requireMfa != null) 'require_mfa': requireMfa,
      },
      reauth: requireMfa != null,
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }

  /// Leaves an organization (allowed without a second factor;
  /// `last_org_manager` for the last manager).
  Future<void> leave(String id, {RequestOptions? options}) async {
    await client.request(RequestInit(
      method: 'POST',
      path: ['_orgs', id, 'leave'],
      headers: options?.headers,
      retry: options?.retry,
    ));
  }

  /// Deletes an organization: access ends at once and a job cleans up its
  /// documents. Needs `manage: org` and a fresh proof of identity: wrap the
  /// call in `client.auth.withProof`.
  Future<void> delete(String id, {RequestOptions? options}) async {
    await client.request(RequestInit(
      method: 'DELETE',
      path: ['_orgs', id],
      reauth: true,
      headers: options?.headers,
      retry: options?.retry,
    ));
  }

  /// The members of an organization: `list`, `update` (direct roles) and
  /// `remove`.
  OrgMembers members(String org) => OrgMembers(client, org);

  /// The organization's own versions of the realm's emails
  /// (`email.customizable`): `list`, `get`, `put`, `delete`, `preview` and
  /// `test`.
  OrgTemplates templates(String org) => OrgTemplates(client, org);

  /// The teams of an organization: groups of members that carry roles.
  OrgTeams teams(String org) => OrgTeams(client, org);

  /// The invitations of an organization.
  OrgInvitations invitations(String org) => OrgInvitations(client, org);

  /// What a token offers (the organization, roles and teams), without
  /// taking it: "Join Acme as editor?". Needs a session. An unknown,
  /// expired, revoked or used-up token is `invalid_invitation`; a token
  /// bound to someone else is `invitation_not_for_you`.
  Future<Map<String, Object?>> previewInvitation(String token,
      {RequestOptions? options}) async {
    final res = await client.request(RequestInit(
      method: 'POST',
      path: const ['_orgs', 'invitations', 'preview'],
      body: {'token': token},
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }

  /// Takes an invitation: the user becomes a member, or gains its roles and
  /// teams.
  Future<Map<String, Object?>> acceptInvitation(String token,
      {RequestOptions? options}) async {
    final res = await client.request(RequestInit(
      method: 'POST',
      path: const ['_orgs', 'invitations', 'accept'],
      body: {'token': token},
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }
}

/// The members of one organization.
final class OrgMembers {
  OrgMembers(this.client, this.org);

  final BackdClient<Object?> client;
  final String org;

  /// A page of members sorted by user id: `{items, has_more, next}`; pass
  /// `next` as [after].
  Future<Map<String, Object?>> list({
    int? limit,
    String? after,
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'GET',
      path: ['_orgs', org, 'members'],
      query: {'limit': limit, 'after': after},
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }

  /// Replaces a member's direct roles. With `manage: members` only roles
  /// that open nothing the caller lacks; with `manage: roles` any.
  Future<Map<String, Object?>> update(String user, List<String> roles,
      {RequestOptions? options}) async {
    final res = await client.request(RequestInit(
      method: 'PATCH',
      path: ['_orgs', org, 'members', user],
      body: {'roles': roles},
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }

  /// Removes a member; their access ends at once.
  Future<void> remove(String user, {RequestOptions? options}) async {
    await client.request(RequestInit(
      method: 'DELETE',
      path: ['_orgs', org, 'members', user],
      headers: options?.headers,
      retry: options?.retry,
    ));
  }
}

/// The teams of one organization.
final class OrgTeams {
  OrgTeams(this.client, this.org);

  final BackdClient<Object?> client;
  final String org;

  Future<List<Map<String, Object?>>> list({RequestOptions? options}) async {
    final res = await client.request(RequestInit(
      method: 'GET',
      path: ['_orgs', org, 'teams'],
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return [
      for (final t in (res.data as Map?)?['items'] as List? ?? const [])
        if (t is Map) Map<String, Object?>.from(t),
    ];
  }

  Future<Map<String, Object?>> create(
    Map<String, Object?> input, {
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'POST',
      path: ['_orgs', org, 'teams'],
      body: input,
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }

  Future<Map<String, Object?>> update(
    String team,
    Map<String, Object?> patch, {
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'PATCH',
      path: ['_orgs', org, 'teams', team],
      body: patch,
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }

  /// Deletes a team: it leaves every membership, and its roles go with it.
  Future<void> delete(String team, {RequestOptions? options}) async {
    await client.request(RequestInit(
      method: 'DELETE',
      path: ['_orgs', org, 'teams', team],
      headers: options?.headers,
      retry: options?.retry,
    ));
  }

  /// The members of a team and the holders of its team roles, in force now.
  /// With [role], only the holders of that role (members of the
  /// organization who need not belong to the team).
  Future<Map<String, Object?>> members(
    String team, {
    String? role,
    int? limit,
    String? after,
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'GET',
      path: ['_orgs', org, 'teams', team, 'members'],
      query: {'role': role, 'limit': limit, 'after': after},
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }

  /// Gives a member a team role in a team: with `manage: teams`, any;
  /// otherwise a holder of a team role of that team whose `grants` lists
  /// it. `from` and `until` set the window it counts in.
  /// `OrgRoleRequiresMFAError` when the role that lets the caller does and
  /// the session has no second factor.
  Future<Map<String, Object?>> grantRole(
    String team,
    Map<String, Object?> input, {
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'POST',
      path: ['_orgs', org, 'teams', team, 'roles'],
      body: input,
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }

  /// Replaces the window of a team role the member holds ([NotFoundError]
  /// if they don't).
  Future<Map<String, Object?>> setRoleWindow(
    String team,
    Map<String, Object?> input, {
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'PATCH',
      path: ['_orgs', org, 'teams', team, 'roles'],
      body: input,
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }

  /// Takes a team role from a member: access through it ends at their next
  /// request.
  Future<Map<String, Object?>> revokeRole(
    String team,
    Map<String, Object?> input, {
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'DELETE',
      path: ['_orgs', org, 'teams', team, 'roles'],
      body: input,
      headers: options?.headers,
      retry: options?.retry,
    ));
    return res.data is Map
        ? Map<String, Object?>.from(res.data as Map)
        : const {};
  }

  /// Puts a member of the organization in a team; they gain its roles.
  Future<Map<String, Object?>> addMember(
    String team,
    String user, {
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'PUT',
      path: ['_orgs', org, 'teams', team, 'members', user],
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }

  /// Takes a member out of a team.
  Future<Map<String, Object?>> removeMember(
    String team,
    String user, {
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'DELETE',
      path: ['_orgs', org, 'teams', team, 'members', user],
      headers: options?.headers,
      retry: options?.retry,
    ));
    return res.data is Map
        ? Map<String, Object?>.from(res.data as Map)
        : const {};
  }
}

/// The email templates of one organization: its own text for the kinds the
/// realm lets companies customize, in the logic-less syntax. Needs the
/// capability `email.overrides.permission` names (`manage: org` by
/// default).
final class OrgTemplates {
  OrgTemplates(this.client, this.org);

  final BackdClient<Object?> client;
  final String org;

  /// The kinds that can be customized, with the placeholders each must
  /// print, and the organization's overrides (without their text).
  Future<Map<String, Object?>> list({RequestOptions? options}) async {
    final res = await client.request(RequestInit(
      method: 'GET',
      path: ['_orgs', org, 'templates'],
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }

  /// One template with its versions; [NotFoundError] if the organization
  /// has none.
  Future<Map<String, Object?>> get(
    String kind,
    String locale, {
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'GET',
      path: ['_orgs', org, 'templates', kind, locale],
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }

  /// Saves a version. A template with a problem rejects with a
  /// [ValidationError] (`invalid_template`) whose `details` say where
  /// (`text`: `line 3, column 5: …`).
  Future<Map<String, Object?>> put(
    String kind,
    String locale,
    Map<String, Object?> template, {
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'PUT',
      path: ['_orgs', org, 'templates', kind, locale],
      body: template,
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }

  /// Goes back to the realm's own template of the kind.
  Future<void> delete(
    String kind,
    String locale, {
    RequestOptions? options,
  }) async {
    await client.request(RequestInit(
      method: 'DELETE',
      path: ['_orgs', org, 'templates', kind, locale],
      headers: options?.headers,
      retry: options?.retry,
    ));
  }

  /// Renders a draft, or the saved version without one, without sending
  /// anything. `data: 'real'` calls the realm's data hook.
  Future<Map<String, Object?>> preview(
    String kind,
    String locale, {
    Map<String, Object?>? draft,
    String? data,
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'POST',
      path: ['_orgs', org, 'templates', kind, locale, 'preview'],
      body: {...?draft, 'data': data},
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }

  /// Sends a draft (or the saved version) to your own address, marked
  /// [TEST]. At most 10 per hour for the organization (`RetryableError`,
  /// 429).
  Future<void> test(
    String kind,
    String locale, {
    Map<String, Object?>? draft,
    RequestOptions? options,
  }) async {
    await client.request(RequestInit(
      method: 'POST',
      path: ['_orgs', org, 'templates', kind, locale, 'test'],
      body: draft ?? const {},
      headers: options?.headers,
      retry: options?.retry,
    ));
  }
}

/// The invitations of one organization.
final class OrgInvitations {
  OrgInvitations(this.client, this.org);

  final BackdClient<Object?> client;
  final String org;

  /// Invites. The answer holds the `token` and the `link` (the realm's
  /// `invitation_link`), shown only now; `send: true` also emails an
  /// `email` binding.
  ///
  /// [binding]: `user` (one user id), `email` (whoever holds a verified
  /// address) or `none` (anyone signed in with the link).
  Future<Map<String, Object?>> create({
    required String binding,
    String? user,
    String? email,
    List<String>? roles,
    List<String>? teams,
    bool? send,
    String? expiresIn,
    int? maxUses,
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'POST',
      path: ['_orgs', org, 'invitations'],
      body: {
        'binding': binding,
        if (user != null) 'user': user,
        if (email != null) 'email': email,
        if (roles != null) 'roles': roles,
        if (teams != null) 'teams': teams,
        if (send != null) 'send': send,
        if (expiresIn != null) 'expires_in': expiresIn,
        if (maxUses != null) 'max_uses': maxUses,
      },
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }

  /// Status, binding, uses and expiry, newest first; never tokens.
  Future<List<Map<String, Object?>>> list({RequestOptions? options}) async {
    final res = await client.request(RequestInit(
      method: 'GET',
      path: ['_orgs', org, 'invitations'],
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return [
      for (final i in (res.data as Map?)?['items'] as List? ?? const [])
        if (i is Map) Map<String, Object?>.from(i),
    ];
  }

  Future<void> revoke(String id, {RequestOptions? options}) async {
    await client.request(RequestInit(
      method: 'DELETE',
      path: ['_orgs', org, 'invitations', id],
      headers: options?.headers,
      retry: options?.retry,
    ));
  }

  /// A new token and link for a pending invitation; the old link stops
  /// working.
  Future<Map<String, Object?>> newLink(String id,
      {RequestOptions? options}) async {
    final res = await client.request(RequestInit(
      method: 'POST',
      path: ['_orgs', org, 'invitations', id, 'link'],
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }

  /// Sends the email of an invitation bound to an address again.
  Future<void> send(String id, {RequestOptions? options}) async {
    await client.request(RequestInit(
      method: 'POST',
      path: ['_orgs', org, 'invitations', id, 'send'],
      headers: options?.headers,
      retry: options?.retry,
    ));
  }
}
