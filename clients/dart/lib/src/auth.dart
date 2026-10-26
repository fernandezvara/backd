import 'dart:async';
import 'dart:convert';

import 'package:meta/meta.dart';

import 'client.dart';
import 'errors.dart';
import 'oauth.dart';
import 'storage.dart';

/// A realm user as the user sees themselves.
final class User {
  const User({
    required this.id,
    required this.email,
    required this.emailVerified,
    required this.roles,
    required this.locale,
    required this.createdAt,
  });

  factory User.fromJson(Map<String, Object?> json) => User(
        id: json['id'] as String,
        email: json['email'] as String,
        emailVerified: json['email_verified'] as bool? ?? false,
        roles: [for (final r in json['roles'] as List? ?? const []) '$r'],
        locale: json['locale'] as String? ?? '',
        createdAt: json['created_at'] as String? ?? '',
      );

  final String id;
  final String email;
  final bool emailVerified;
  final List<String> roles;

  /// The user's language, one the realm lists (used for their emails).
  final String locale;

  /// RFC3339 timestamp.
  final String createdAt;
}

/// One way the user signs in: a password or an external provider.
final class Identity {
  const Identity({
    required this.provider,
    required this.email,
    required this.emailVerified,
    required this.createdAt,
    this.lastUsedAt,
  });

  factory Identity.fromJson(Map<String, Object?> json) => Identity(
        provider: json['provider'] as String,
        email: json['email'] as String? ?? '',
        emailVerified: json['email_verified'] as bool? ?? false,
        createdAt: json['created_at'] as String? ?? '',
        lastUsedAt: json['last_used_at'] as String?,
      );

  /// `password`, `google`, `microsoft`, `apple` or a configured provider's
  /// name.
  final String provider;

  /// The address the provider reported (the user's own for a password).
  final String email;

  final bool emailVerified;

  /// RFC3339 timestamp: when it was linked.
  final String createdAt;

  /// RFC3339 timestamp of the last sign-in with it, null if none is recorded.
  final String? lastUsedAt;
}

/// The signed-in user with their sign-in methods, oldest first.
/// With `consents` declared in the realm it also has [consents] (name to the
/// version held) and [toAccept] (the required ones still to accept).
final class Me extends User {
  const Me({
    required super.id,
    required super.email,
    required super.emailVerified,
    required super.roles,
    required super.locale,
    required super.createdAt,
    required this.identities,
    required this.reauthMethods,
    required this.signInMethods,
    this.consents = const {},
    this.toAccept = const [],
    this.org,
  });

  factory Me.fromJson(Map<String, Object?> json) => Me(
        id: json['id'] as String,
        email: json['email'] as String,
        emailVerified: json['email_verified'] as bool? ?? false,
        roles: [for (final r in json['roles'] as List? ?? const []) '$r'],
        locale: json['locale'] as String? ?? '',
        createdAt: json['created_at'] as String? ?? '',
        identities: [
          for (final i in json['identities'] as List? ?? const [])
            if (i is Map) Identity.fromJson(Map<String, Object?>.from(i)),
        ],
        reauthMethods: [
          for (final m in json['reauth_methods'] as List? ?? const []) '$m',
        ],
        signInMethods: [
          for (final m in json['sign_in_methods'] as List? ?? const []) '$m',
        ],
        consents: {
          for (final e in (json['consents'] as Map? ?? const {}).entries)
            '${e.key}': '${e.value}',
        },
        toAccept: [
          for (final c in json['to_accept'] as List? ?? const [])
            if (c is Map)
              PendingConsent(
                name: '${c['name']}',
                version: '${c['version']}',
              ),
        ],
        org: json['org'] is Map
            ? Map<String, Object?>.from(json['org'] as Map)
            : null,
      );

  /// Only for a session scoped to an organization: the organization and what
  /// the user is there (`id`, `name`, `slug`, `roles`, `teams`).
  final Map<String, Object?>? org;

  /// The user's ways to sign in, oldest first.
  final List<Identity> identities;

  /// The proofs the user can give: `totp`, `recovery_code`, `password`,
  /// `email_code`.
  final List<String> reauthMethods;

  /// `password` and/or the providers.
  final List<String> signInMethods;

  /// Consent name → the version held.
  final Map<String, String> consents;

  /// The required consents still to accept.
  final List<PendingConsent> toAccept;
}

/// What a provider says of a person who just signed up, handed over once:
/// fields the provider did not give are absent. Treat it as a hint.
final class Profile {
  const Profile({this.name, this.givenName, this.familyName, this.picture});

  factory Profile.fromJson(Map<String, Object?> json) => Profile(
        name: json['name'] as String?,
        givenName: json['given_name'] as String?,
        familyName: json['family_name'] as String?,
        picture: json['picture'] as String?,
      );

  final String? name;
  final String? givenName;
  final String? familyName;

  /// A URL.
  final String? picture;
}

/// What a first factor leads to: a session, or the second step the user has
/// to take.
sealed class SignInResult {
  const SignInResult();
}

/// A new session.
final class Session extends SignInResult {
  const Session({
    this.token,
    this.tokenType,
    required this.sessionId,
    required this.expiresAt,
    required this.user,
    this.newUser,
    this.profile,
    this.recoveryCodesLeft,
    this.org,
  });

  /// The session of a sign-in, from the server's answer.
  factory Session.fromJson(Map<String, Object?> json) => Session(
        token: json['token'] as String?,
        tokenType: json['token_type'] as String?,
        sessionId: json['session_id'] as String? ?? '',
        expiresAt: json['expires_at'] as String? ?? '',
        user: json['user'] is Map
            ? User.fromJson(Map<String, Object?>.from(json['user'] as Map))
            : null,
        newUser: json['new_user'] as bool?,
        profile: json['profile'] is Map
            ? Profile.fromJson(Map<String, Object?>.from(json['profile'] as Map))
            : null,
        recoveryCodesLeft: json['recovery_codes_left'] as int?,
        org: json['org'] as String?,
      );

  /// Session token (`bds_…`); already stored by the client.
  final String? token;

  /// `Bearer` when present.
  final String? tokenType;

  final String sessionId;

  /// RFC3339; pushed forward as the session is used.
  final String expiresAt;

  /// The signed-in user; null in the answers that carry none.
  final User? user;

  /// With a provider sign-in: whether the account was just made.
  final bool? newUser;

  /// With a provider sign-up: what the provider said, once.
  final Profile? profile;

  /// After `recovery_code`: how many remain.
  final int? recoveryCodesLeft;

  /// The organization the session is scoped to: its token reaches that
  /// organization and no other.
  final String? org;
}

/// A sign-in that needs a code of the user's second factor before it gives a
/// session: send it with [Auth.verifyMfa].
final class MfaChallenge extends SignInResult {
  const MfaChallenge({required this.challenge, this.methods = const []});

  final String challenge;

  /// `totp`, `recovery_code`.
  final List<String> methods;
}

/// A sign-in of a user whose role requires a second factor and who has none:
/// set one up with `auth.mfa.setup(challenge: …)` and
/// `auth.mfa.confirm(setupId: …, code: …, challenge: …)`, which gives the
/// session.
final class EnrollmentChallenge extends SignInResult {
  const EnrollmentChallenge({required this.challenge});

  final String challenge;
}

/// A sign-in that is done but has consents to accept first (a required one
/// never accepted, or a new version that asks for re-acceptance): show their
/// texts (`client.consents.text`) and send the acceptance with
/// [Auth.acceptConsents].
final class ConsentChallenge extends SignInResult {
  const ConsentChallenge({required this.challenge, required this.consents});

  final String challenge;
  final List<PendingConsent> consents;
}

/// A provider was linked to the signed-in user.
final class LinkedResult extends SignInResult {
  const LinkedResult(this.provider);

  final String provider;
}

/// What changed: a sign-in (signup or login), a sign-out (logout, logout
/// everywhere, account deleted), or the server refusing the stored token
/// (expired, revoked, or the user disabled).
enum AuthEvent { signedIn, signedOut, sessionExpired }

/// One change of the sign-in state, from [Auth.events].
final class AuthChange {
  const AuthChange(this.event, this.session);

  final AuthEvent event;

  /// The new session for [AuthEvent.signedIn], otherwise null.
  final Session? session;
}

/// One of the user's sessions.
final class SessionInfo {
  const SessionInfo({
    required this.id,
    required this.createdAt,
    required this.lastUsedAt,
    required this.expiresAt,
    required this.current,
    this.org,
    this.connection,
  });

  factory SessionInfo.fromJson(Map<String, Object?> json) => SessionInfo(
        id: json['id'] as String,
        createdAt: json['created_at'] as String? ?? '',
        lastUsedAt: json['last_used_at'] as String? ?? '',
        expiresAt: json['expires_at'] as String? ?? '',
        current: json['current'] as bool? ?? false,
        org: json['org'] as String?,
        connection: json['connection'] as String?,
      );

  final String id;
  final String createdAt;
  final String lastUsedAt;
  final String expiresAt;

  /// The id of the organization the session is scoped to (null for an
  /// operator's).
  final String? org;

  /// The provider of the organization's connection that opened it.
  final String? connection;

  /// The session making this request.
  final bool current;
}

/// The answer of `auth.mfa.status`.
final class MfaStatus {
  const MfaStatus({
    required this.available,
    required this.enabled,
    required this.methods,
    required this.recoveryCodesLeft,
    required this.required,
  });

  factory MfaStatus.fromJson(Map<String, Object?> json) => MfaStatus(
        available: json['available'] as bool? ?? false,
        enabled: json['enabled'] as bool? ?? false,
        methods: [for (final m in json['methods'] as List? ?? const []) '$m'],
        recoveryCodesLeft: json['recovery_codes_left'] as int? ?? 0,
        required: json['required'] as bool? ?? false,
      );

  final bool available;
  final bool enabled;
  final List<String> methods;
  final int recoveryCodesLeft;
  final bool required;
}

/// The answer of `auth.mfa.setup`: the secret, the `otpauth_uri` and `qr`
/// (a PNG data URI).
final class MfaSetup {
  const MfaSetup({
    required this.setupId,
    required this.secret,
    required this.otpauthUri,
    required this.qr,
    required this.expiresAt,
  });

  factory MfaSetup.fromJson(Map<String, Object?> json) => MfaSetup(
        setupId: json['setup_id'] as String,
        secret: json['secret'] as String,
        otpauthUri: json['otpauth_uri'] as String? ?? '',
        qr: json['qr'] as String? ?? '',
        expiresAt: json['expires_at'] as String? ?? '',
      );

  final String setupId;
  final String secret;
  final String otpauthUri;
  final String qr;
  final String expiresAt;
}

/// The answer of `auth.mfa.confirm`: the `recovery_codes`, shown once; with
/// a challenge also the session, or the consent step still to take.
final class MfaConfirmResult {
  const MfaConfirmResult({
    required this.recoveryCodes,
    this.session,
    this.consent,
  });

  /// Recovery codes, shown once.
  final List<String> recoveryCodes;

  /// The session the sign-in ended in.
  final Session? session;

  /// The consent step, when consents still need accepting.
  final ConsentChallenge? consent;
}

/// A body without the fields that were not given.
Map<String, Object?> _compact(Map<String, Object?> fields) =>
    {for (final e in fields.entries) if (e.value != null) e.key: e.value};

/// Sign-up, login and sessions: `client.auth`.
final class Auth {
  Auth(this.client) : ready = _ready(client) {
    client.onSessionExpired = sessionExpired;
  }

  final BackdClient<Object?> client;

  /// Completes once the stored credentials have been read: on a platform with
  /// no address bar the client never finishes a redirect on its own, so this
  /// resolves to null; the app calls [completeSignIn] with the URI it
  /// received (a deep link or the loopback callback).
  final Future<SignInResult?> ready;

  /// A completed redirect's outcome: a second call with no URI resolves to
  /// the same one, for code that runs after the URI was consumed.
  Future<SignInResult?>? _completed;

  OAuthStorage? _oauthStorage;

  /// Sign-in, sign-out and session expiry, as a broadcast stream.
  final _events = StreamController<AuthChange>.broadcast();

  /// The session this client signed in with, as the sign-in answered
  /// ([Session.org] is the organization it is scoped to), or null. It is kept
  /// in memory only: a client that has just started knows nothing until it
  /// asks the server ([me], [org]).
  Session? current;

  /// The path of the sign-in routes for an organization: the one given, else
  /// the client's own, else the realm's.
  @internal
  List<String> base([String? org]) {
    final ref = org?.trim().isNotEmpty == true ? org!.trim() : client.orgRef;
    return ref.isEmpty ? const ['_auth'] : ['_auth', 'orgs', ref];
  }

  /// What changed on sign-in, sign-out or session expiry.
  Stream<AuthChange> get events => _events.stream;

  /// The signed-in user's second factor: `client.auth.mfa`. Changes need a
  /// fresh proof: see [reauth].
  late final Mfa mfa = Mfa(this);

  /// Sign-in by an emailed link and code: `client.auth.magicLink`.
  late final MagicLink magicLink = MagicLink(this);

  /// Setting and removing the password of an account that may sign in
  /// another way.
  late final PasswordAuth password = PasswordAuth(this);

  static Future<SignInResult?> _ready(BackdClient<Object?> client) async {
    await client.storage.get();
    return null;
  }

  /// Where the PKCE verifier waits while the user is at the provider; the
  /// client's `oauthStorage`, memory by default.
  OAuthStorage get oauthStorage =>
      client.oauthStorage ?? (_oauthStorage ??= MemoryOAuthStorage());
  set oauthStorage(OAuthStorage storage) => _oauthStorage = storage;

  /// The organization this session is scoped to, and what the person is there
  /// (`id`, `name`, `slug`, `roles`, `teams`); null for a session that is not
  /// scoped. It asks the server, so it is right after a restart too. The name
  /// is kept for [Orgs.signedIn].
  Future<Map<String, Object?>?> org({RequestOptions? options}) async {
    final res = await client.request(RequestInit(
      method: 'GET',
      path: const ['_auth', 'me'],
      retry: options?.retry,
      headers: options?.headers,
      cache: options?.cache ?? true,
    ));
    final org = (res.data as Map?)?['org'];
    if (org is! Map) return null;
    final out = Map<String, Object?>.from(org);
    if (out['name'] != null) {
      await _remember({'name': out['name'], 'org': out['id']});
    }
    return out;
  }

  /// What an organization's sign-in page can offer: the providers (see
  /// [providers]) and the other ways in, `password` and, when the realm has
  /// them, `magic_link`. [org] defaults to the client's own; without either
  /// the realm's providers are listed and `methods` is empty. [ssoRequired]
  /// says the organization's people sign in through its connection only: then
  /// `providers` holds just its buttons and `methods` is empty.
  Future<({List<String> providers, List<String> methods, bool ssoRequired})>
      options({
    String? org,
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'GET',
      path: [...base(org), 'providers'],
      auth: false,
      retry: options?.retry,
      headers: options?.headers,
      cache: options?.cache ?? true,
    ));
    final data = res.data as Map? ?? const {};
    return (
      providers: [for (final p in data['items'] as List? ?? const []) '$p'],
      methods: [for (final m in data['methods'] as List? ?? const []) '$m'],
      ssoRequired: data['sso_required'] == true,
    );
  }

  /// The providers the app may offer right now, for the realm or, with
  /// [org], for one organization (the realm's own and the organization's
  /// connections): only those that are configured and usable. An unknown
  /// organization answers like one without connections, so the list says
  /// nothing about who exists.
  Future<List<String>> providers(
          {String? org, RequestOptions? options}) async =>
      (await this.options(org: org, options: options)).providers;

  /// Creates an account and an organization for a new person and signs in to
  /// it: how a company starts. The realm's sign-up must be open; [slug] is
  /// the name people will type to sign in (`acme-corp`) and is unique
  /// (`slug_taken`, a [ConflictError], makes nothing). The session is scoped
  /// to the new organization and stored by the client; the answer holds it
  /// with the organization. A realm that verifies addresses first creates
  /// both and throws a [VerificationRequiredError]. [invitation] is the realm's
  /// invitation, for a realm that is invite-only.
  Future<({Session session, Map<String, Object?> organization})> signUpWithOrg(
    String email,
    String password, {
    required String name,
    required String slug,
    String? invitation,
    String? locale,
    String? redirectTo,
    Map<String, String>? consents,
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'POST',
      path: const ['_auth', 'orgs'],
      auth: false,
      body: {
        'email': email,
        'password': password,
        'name': name,
        'slug': slug,
        ..._compact({
          'invitation': invitation,
          'locale': locale,
          'redirect_to': redirectTo,
          'consents': consents,
        }),
      },
      retry: options?.retry,
      headers: options?.headers,
      cache: options?.cache ?? true,
    ));
    if (res.status == 202) {
      throw VerificationRequiredError(
        status: res.status,
        code: 'verification_required',
        message:
            'the account and the organization were created: verify the email address, then sign in',
      );
    }
    final data = Map<String, Object?>.from(res.data as Map);
    final organization =
        Map<String, Object?>.from(data.remove('organization') as Map);
    // The session is the new organization's: kept under its own client (named
    // as the person will type it: the slug) when the storage can, else in
    // this one. A person whose role needs a second factor, or consents, has a
    // step to take first: that throws a [SecondStepRequiredError].
    final target = client.keepsOrgSessions ? client.org(slug).auth : this;
    return (
      session: target.session(await target.step(data)),
      organization: organization
    );
  }

  /// Asks the server to email an address the organizations it belongs to,
  /// each with the link to its sign-in page: for someone on a new device who
  /// does not remember the company. Always resolves, whatever the address is,
  /// so it tells the caller nothing; the realm needs
  /// `organizations.signin_link`.
  Future<void> findOrgs(String email,
          {String? locale, RequestOptions? options}) =>
      _accepted(const ['_auth', 'orgs', 'find'],
          {'email': email, 'locale': locale}, options);

  /// What an invitation to an organization offers, before the person has
  /// signed in: the organization (`org` with `id` and `name`), `roles`,
  /// `teams`, `binding` and `expires_at`, so the app can ask "Join Acme as
  /// editor?". Every token that does not work fails alike with
  /// `invalid_invitation`. [org] is the organization the token is for, by id
  /// or slug (the client's own by default).
  Future<Map<String, Object?>> previewInvitation(
    String token, {
    String? org,
    RequestOptions? options,
  }) async {
    final base = this.base(org);
    if (base.length < 3) {
      throw ArgumentError(
          'previewInvitation: the organization is required (the client\'s org or the org argument)');
    }
    final res = await client.request(RequestInit(
      method: 'POST',
      path: [...base, 'invitations', 'preview'],
      auth: false,
      body: {'token': token},
      retry: options?.retry,
      headers: options?.headers,
      cache: options?.cache ?? true,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }

  /// What a first factor answered: the session (stored), or the second step
  /// the user has to take.
  Future<SignInResult> step(Object? data) async {
    if (data is Map) {
      final map = Map<String, Object?>.from(data);
      if (map['mfa_required'] == true) {
        return MfaChallenge(
          challenge: map['challenge'] as String,
          methods: [for (final m in map['methods'] as List? ?? const []) '$m'],
        );
      }
      if (map['mfa_enrollment_required'] == true) {
        return EnrollmentChallenge(challenge: map['challenge'] as String);
      }
      if (map['consent_required'] == true) {
        return ConsentChallenge(
          challenge: map['challenge'] as String,
          consents: [
            for (final c in map['consents'] as List? ?? const [])
              if (c is Map)
                PendingConsent(name: '${c['name']}', version: '${c['version']}'),
          ],
        );
      }
      return _signedIn(Session.fromJson(map));
    }
    throw ValidationError(
      status: 200,
      code: 'unexpected_answer',
      message: 'the sign-in answer is not one the client understands',
    );
  }

  /// Turns a step into the session a method promises, or throws
  /// [SecondStepRequiredError].
  Session session(SignInResult result) {
    if (result is ConsentChallenge) {
      throw SecondStepRequiredError(
        status: 200,
        code: 'consent_required',
        challenge: result.challenge,
        consents: result.consents,
        message:
            'consents must be accepted: show client.consents.text(name, version) and call auth.acceptConsents(challenge, consents)',
      );
    }
    if (result case MfaChallenge(challenge: final c, methods: final m)) {
      throw SecondStepRequiredError(
        status: 200,
        code: 'mfa_required',
        challenge: c,
        methods: m,
        message:
            'a code of the second factor is needed: auth.verifyMfa(challenge, method, code)',
      );
    }
    if (result case EnrollmentChallenge(challenge: final c)) {
      throw SecondStepRequiredError(
        status: 200,
        code: 'mfa_enrollment_required',
        challenge: c,
        message:
            'this account must set up a second factor: auth.mfa.setup(challenge: …)',
      );
    }
    if (result is Session) return result;
    throw StateError('a sign-in result that is not a session has no session');
  }

  /// Creates an account and signs in. Realms with `signup: invite` need an
  /// invitation token. `locale` (such as `es` or `es-MX`) is the language to
  /// use for the user; the server maps it silently to one the realm lists.
  /// `redirectTo` is where the page after the verification link may send the
  /// user (it must be within the realm's `email.allowed_redirects`).
  ///
  /// A realm that requires verified addresses
  /// (`account.require_verified_email`) creates the account but starts no
  /// session: this throws a [VerificationRequiredError], nothing is stored,
  /// and the user signs in after following the link in the email they were
  /// sent.
  /// A user whose role requires a second factor has no session yet: this
  /// throws a [SecondStepRequiredError] (`mfa_enrollment_required`) carrying
  /// the challenge.
  /// `consents` are the consents accepted, as the consent's name and the
  /// version shown (`{terms: '2026-10'}`): every required one of the realm
  /// must be there at its current version, or this throws a
  /// [ConsentRequiredError] that lists them.
  /// `org` is the organization the person joins (an id or a slug; the
  /// client's own when it was made with one): the sign-up goes to
  /// `/_auth/orgs/{org}/signup`, which needs the organization's `invitation`,
  /// and the session is scoped to the organization.
  Future<Session> signup(
    String email,
    String password, {
    String? invitation,
    String? locale,
    String? redirectTo,
    String? org,
    Map<String, String>? consents,
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'POST',
      path: [...base(org), 'signup'],
      auth: false,
      body: {
        'email': email,
        'password': password,
        ..._compact({
          'consents': consents,
          'invitation': invitation,
          'locale': locale,
          'redirect_to': redirectTo,
        }),
      },
      retry: options?.retry,
      headers: options?.headers,
      cache: options?.cache ?? true,
    ));
    if (res.status == 202) {
      throw VerificationRequiredError(
        status: res.status,
        code: 'verification_required',
        message:
            'the account was created: verify the email address, then sign in',
      );
    }
    return session(await step(res.data));
  }

  /// Signs in with email and password. The answer is the session, or, for a
  /// user with a second factor, an [MfaChallenge] to finish with
  /// [verifyMfa], or an [EnrollmentChallenge] — or a [ConsentChallenge] to
  /// finish with [acceptConsents]. Nothing is stored until a session.
  /// `org` is the organization the person signs in to (an id or a slug; the
  /// client's own when it was made with one): the session, or the second
  /// step, is scoped to it. A person who is not a member, an organization
  /// that is not there and a wrong password all fail alike, with
  /// `invalid_credentials`. `invitation` is an invitation token of the
  /// organization, used as part of the sign-in.
  Future<SignInResult> signIn(
    String email,
    String password, {
    String? org,
    String? invitation,
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'POST',
      path: [...base(org), 'login'],
      auth: false,
      body: _compact(
          {'email': email, 'password': password, 'invitation': invitation}),
      retry: options?.retry,
      headers: options?.headers,
      cache: options?.cache ?? true,
    ));
    return step(res.data);
  }

  /// Signs in with email and password and promises a session: a user who
  /// needs a second step makes this throw a [SecondStepRequiredError] (use
  /// [signIn] to get the step as a value).
  Future<Session> login(
    String email,
    String password, {
    String? org,
    String? invitation,
    RequestOptions? options,
  }) async =>
      session(await signIn(email, password,
          org: org, invitation: invitation, options: options));

  /// Starts signing in with a provider the realm lists (`google`,
  /// `microsoft` or `apple`): makes a PKCE verifier, keeps it for the return,
  /// and answers with the provider's address for the app to open (the system
  /// browser). The user comes back to [redirectTo] — a deep link or the
  /// loopback listener's address — where [completeSignIn] finishes the
  /// sign-in. Without `redirectTo` the realm's `sign_in.app_url` is where the
  /// flow ends (or the server refuses).
  ///
  /// [navigate], when given, is how the app opens the address (a
  /// `url_launcher` call on Flutter); by default nothing is opened and the
  /// returned URL is the app's to handle.
  Future<({String authorizeUrl})> signInWith(
    String provider, {
    String? redirectTo,
    String? invitation,
    String? locale,
    String? org,
    Map<String, String>? consents,
    FutureOr<void> Function(Uri url)? navigate,
    RequestOptions? options,
  }) =>
      _startOAuth(provider, 'signin',
          org: org,
          redirectTo: redirectTo,
          invitation: invitation,
          locale: locale,
          consents: consents,
          navigate: navigate,
          options: options);

  /// Adds a provider to the signed-in user as another way to sign in. The
  /// same flow as [signInWith]; [completeSignIn] then resolves with a
  /// [LinkedResult].
  Future<({String authorizeUrl})> linkProvider(
    String provider, {
    String? redirectTo,
    String? invitation,
    String? locale,
    Map<String, String>? consents,
    FutureOr<void> Function(Uri url)? navigate,
    RequestOptions? options,
  }) =>
      _startOAuth(provider, 'link',
          redirectTo: redirectTo,
          invitation: invitation,
          locale: locale,
          consents: consents,
          navigate: navigate,
          options: options);

  Future<({String authorizeUrl})> _startOAuth(
    String provider,
    String intent, {
    String? org,
    String? redirectTo,
    String? invitation,
    String? locale,
    Map<String, String>? consents,
    FutureOr<void> Function(Uri url)? navigate,
    RequestOptions? options,
  }) async {
    final (:verifier, :challenge) = newPKCE();
    final res = await client.request(RequestInit(
      method: 'POST',
      path: [
        ...(intent == 'link' ? const ['_auth'] : base(org)),
        'oauth',
        provider,
        'start',
      ],
      auth: intent == 'link',
      body: _compact({
        'redirect_to': redirectTo,
        'code_challenge': challenge,
        'intent': intent,
        'invitation': invitation,
        'locale': locale,
        'consents': consents,
      }),
      retry: options?.retry,
      headers: options?.headers,
      cache: options?.cache ?? true,
    ));
    await savePending(oauthStorage,
        verifier: verifier, provider: provider, intent: intent);
    final authorizeUrl = (res.data as Map)['authorize_url'] as String;
    if (navigate != null) await navigate(Uri.parse(authorizeUrl));
    return (authorizeUrl: authorizeUrl);
  }

  /// Finishes a sign-in with a provider at the address backd sent the user
  /// back to: the app hands over the URI it received (a deep link or the
  /// loopback listener's callback). A sign-in is traded for a session
  /// (stored by the client, like a login's) and resolves with it, with
  /// [Session.newUser] and, for a new user, the [Session.profile] the
  /// provider gave; linking resolves with a [LinkedResult]; a failure throws
  /// the error the code means (`account_exists` and `link_conflict` are a
  /// [ConflictError], `signup_closed`, `email_not_verified` and
  /// `signin_refused` a [ForbiddenError], `cancelled` an
  /// [AuthenticationError], …). Resolves with null on an ordinary visit that
  /// has nothing to finish. Called with no URI, resolves with the outcome of
  /// the last finished one, so code that runs after the URI was consumed gets
  /// the same answer.
  ///
  /// An address reached on a device the sign-in was not started on has no
  /// PKCE verifier to redeem the code with: that throws a [ValidationError]
  /// whose `code` is `wrong_device`.
  Future<SignInResult?> completeSignIn([Uri? uri, RequestOptions? options]) async {
    if (uri == null) return _completed ?? Future.value(null);
    final ret = parseReturn(uri);
    if (ret == null) return null;
    final completer = Completer<SignInResult?>();
    // An app that never asks for the outcome must not trip the runtime's
    // unhandled-error report: the failure is still there for whoever does.
    _completed = completer.future..ignore();
    try {
      final storage = oauthStorage;
      final pending = await loadPending(storage);
      await storage.remove();
      if (ret.error != null) {
        throw oauthError(ret.error!, ret.provider ?? pending?.provider);
      }
      if (ret.linked != null) {
        final out = LinkedResult(ret.linked!);
        completer.complete(out);
        return out;
      }
      if (pending == null) {
        throw ValidationError(
          status: 400,
          code: 'wrong_device',
          message:
              'this address was not where the sign-in started: finish it on the device that started it',
        );
      }
      final res = await client.request(RequestInit(
        method: 'POST',
        path: const ['_auth', 'token'],
        auth: false,
        body: {'code': ret.code, 'code_verifier': pending.verifier},
        retry: options?.retry,
        headers: options?.headers,
        cache: options?.cache ?? true,
      ));
      // A sign-in to an organization from a client that is not bound to one
      // belongs to that organization's client.
      final target =
          ret.org != null && client.orgRef.isEmpty && client.keepsOrgSessions
              ? client.org(ret.org!).auth
              : this;
      final out = await target.step(res.data);
      completer.complete(out);
      return out;
    } catch (e) {
      completer.completeError(e);
      rethrow;
    }
  }

  /// Signs in with the ID token a mobile app got from the platform sign-in
  /// (Sign in with Apple, Google Sign-In, MSAL), with the raw `nonce` it
  /// started that sign-in with. For Apple, also the `authorizationCode` its
  /// SDK gave. Throws `invalid_token` (an [AuthenticationError]) when the
  /// token does not verify, and `account_exists`, `signup_closed`, … as
  /// [completeSignIn] does.
  Future<SignInResult> signInWithIdToken(
    String provider, {
    required String idToken,
    required String nonce,
    String? authorizationCode,
    String? invitation,
    String? locale,
    Map<String, String>? consents,
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'POST',
      path: ['_auth', 'oauth', provider, 'id-token'],
      auth: false,
      body: _idTokenBody(
        idToken: idToken,
        nonce: nonce,
        authorizationCode: authorizationCode,
        invitation: invitation,
        locale: locale,
        consents: consents,
      ),
      retry: options?.retry,
      headers: options?.headers,
      cache: options?.cache ?? true,
    ));
    return step(res.data);
  }

  /// Adds a provider to the signed-in user with the ID token of the platform
  /// sign-in.
  Future<LinkedResult> linkProviderWithIdToken(
    String provider, {
    required String idToken,
    required String nonce,
    String? authorizationCode,
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'POST',
      path: ['_auth', 'oauth', provider, 'id-token'],
      body: {
        ..._idTokenBody(
          idToken: idToken,
          nonce: nonce,
          authorizationCode: authorizationCode,
        ),
        'intent': 'link',
      },
      retry: options?.retry,
      headers: options?.headers,
      cache: options?.cache ?? true,
    ));
    return LinkedResult('${(res.data as Map)['linked'] ?? provider}');
  }

  Map<String, Object?> _idTokenBody({
    required String idToken,
    required String nonce,
    String? authorizationCode,
    String? invitation,
    String? locale,
    Map<String, String>? consents,
  }) =>
      _compact({
        'id_token': idToken,
        'nonce': nonce,
        'authorization_code': authorizationCode,
        'invitation': invitation,
        'locale': locale,
        'consents': consents,
      });

  /// Removes one of the user's ways to sign in (`password` included). The
  /// last one can't be removed: that throws a [ConflictError] whose `code` is
  /// `last_sign_in_method`.
  Future<void> unlinkProvider(String provider, {RequestOptions? options}) =>
      client.request(RequestInit(
        method: 'DELETE',
        path: ['_auth', 'identities', provider],
        reauth: true,
        retry: options?.retry,
        headers: options?.headers,
        cache: options?.cache ?? true,
      ));

  /// Finishes a sign-in that answered with an [MfaChallenge]: the code of the
  /// user's authenticator app (`totp`) or a recovery code (`recovery_code`).
  /// Resolves with the session (a provider's also has [Session.newUser], a
  /// recovery code [Session.recoveryCodesLeft]). Throws `invalid_code` (a
  /// [ValidationError]; `details` has the attempts left), `invalid_challenge`
  /// after five wrong ones or five minutes, and `mfa_locked` (a
  /// [RetryableError], 429) after ten wrong codes in an hour.
  Future<Session> verifyMfa(
    String challenge,
    String method,
    String code, {
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'POST',
      path: const ['_auth', 'mfa', 'verify'],
      auth: false,
      body: {'challenge': challenge, 'method': method, 'code': code},
      retry: options?.retry,
      headers: options?.headers,
      cache: options?.cache ?? true,
    ));
    return session(await step(res.data));
  }

  /// Finishes a sign-in that answered with a [ConsentChallenge]: the consents
  /// the user accepted, as `{name: version}`, with every one the challenge
  /// named at its version. Resolves with the session (a provider's also has
  /// [Session.newUser]). Throws a [ConsentRequiredError] when one is missing
  /// (the challenge stays valid) and `invalid_challenge` after five minutes.
  Future<Session> acceptConsents(
    String challenge,
    Map<String, String> consents, {
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'POST',
      path: const ['_auth', 'consents', 'accept'],
      auth: false,
      body: {'challenge': challenge, 'consents': consents},
      retry: options?.retry,
      headers: options?.headers,
      cache: options?.cache ?? true,
    ));
    return session(await step(res.data));
  }

  /// Runs a sensitive action, asking the user for a fresh proof when the
  /// server wants one: `ask` gets the proofs the user can give (`totp`,
  /// `recovery_code`, `password` or `email_code`) and resolves with the one
  /// to use, as [reauth] takes it; the action is then tried once more.
  Future<T> withProof<T>(
    FutureOr<({String method, String? code, String? password})> Function(
            List<String> methods)
        ask,
    Future<T> Function() action,
  ) async {
    try {
      return await action();
    } on AuthenticationError catch (err) {
      if (err.code != 'reauth_required') rethrow;
    }
    final me = await this.me();
    final proof = await ask(me.reauthMethods);
    await reauth(proof.method, code: proof.code, password: proof.password);
    return action();
  }

  /// Asks for the verification email again. Always resolves, whatever the
  /// address is (unknown, disabled, already verified), so it can't be used to
  /// find out who is registered. `redirectTo` is where the page after the
  /// link may send the user (within the realm's `email.allowed_redirects`).
  Future<void> resendVerification(
    String email, {
    String? redirectTo,
    RequestOptions? options,
  }) =>
      _accepted(const ['_auth', 'verify-email', 'resend'],
          {'email': email, 'redirect_to': redirectTo}, options);

  /// Verifies an address with the token of the link in the email: for apps
  /// that host their own page (`email.links` in `realm.yaml`). Starts no
  /// session. An expired, used or unknown token throws a [ValidationError]
  /// whose `code` is `invalid_token`.
  Future<void> verifyEmail(String token, {RequestOptions? options}) =>
      _accepted(const ['_auth', 'verify-email'], {'token': token}, options);

  /// Asks for a password reset email. Always resolves, whatever the address
  /// is. Throws a [RetryableError] once the realm's limits for reset requests
  /// (per address and per client) are reached.
  /// `org` is the organization whose page the person asks from (see
  /// [MagicLink.request]): its members get the organization's context in the
  /// email.
  Future<void> requestPasswordReset(
    String email, {
    String? redirectTo,
    String? org,
    RequestOptions? options,
  }) =>
      _accepted(
        [...base(org), 'reset-password', 'request'],
        {'email': email, 'redirect_to': redirectTo},
        options,
      );

  /// Sets a new password with the token of a reset link. Ends every session
  /// of the user, verifies their address and starts no session: log in
  /// afterwards. A password the policy refuses throws a [ValidationError] and
  /// leaves the token usable; a bad token has the `invalid_token` code.
  Future<void> resetPassword(
    String token,
    String password, {
    RequestOptions? options,
  }) =>
      _accepted(const ['_auth', 'reset-password'],
          {'token': token, 'password': password}, options);

  /// Asks to change the signed-in user's email address (realms with
  /// `account.allow_email_change`). Needs a fresh proof: call [reauth] first.
  /// Resolves whether or not the new address is free; nothing changes until
  /// the link sent to the new address is used (see [confirmEmailChange]).
  Future<void> requestEmailChange(
    String newEmail, {
    String? redirectTo,
    RequestOptions? options,
  }) =>
      client.request(RequestInit(
        method: 'POST',
        path: const ['_auth', 'email'],
        reauth: true,
        body: _compact({'new_email': newEmail, 'redirect_to': redirectTo}),
        retry: options?.retry,
        headers: options?.headers,
        cache: options?.cache ?? true,
      ));

  /// Confirms an email change with the token sent to the new address: the
  /// address changes and every session of the user ends.
  Future<void> confirmEmailChange(String token, {RequestOptions? options}) =>
      _accepted(
          const ['_auth', 'confirm-email-change'], {'token': token}, options);

  /// Undoes an email change with the token sent to the old address: restores
  /// it, ends every session and makes the password unusable until it is reset
  /// (a reset email is sent to the restored address).
  Future<void> revertEmailChange(String token, {RequestOptions? options}) =>
      _accepted(
          const ['_auth', 'revert-email-change'], {'token': token}, options);

  /// Accepts an invitation that was emailed (admin `invitations.send`), with
  /// the token of its link: creates the account for the invited address,
  /// already verified, and starts no session. `locale` is the user's
  /// language.
  Future<void> acceptInvitation(
    String token,
    String password, {
    String? locale,
    RequestOptions? options,
  }) =>
      _accepted(
        const ['_auth', 'accept-invitation'],
        {'token': token, 'password': password, 'locale': locale},
        options,
      );

  /// A call that needs no session and answers with no body.
  Future<void> _accepted(
    List<String> path,
    Map<String, Object?> body,
    RequestOptions? options,
  ) =>
      client.request(RequestInit(
        method: 'POST',
        path: path,
        auth: false,
        body: _compact(body),
        retry: options?.retry,
        headers: options?.headers,
        cache: options?.cache ?? true,
      ));

  /// Ends the current session. The stored token is removed even if the server
  /// can't be reached.
  Future<void> logout({RequestOptions? options}) async {
    try {
      await client.request(RequestInit(
        method: 'POST',
        path: const ['_auth', 'logout'],
        expire: false,
        retry: options?.retry,
        headers: options?.headers,
        cache: options?.cache ?? true,
      ));
    } on AuthenticationError {
      // A session the server no longer knows is logged out already.
    } finally {
      await _signedOut(AuthEvent.signedOut);
    }
  }

  /// Ends every session of the user, on every device.
  Future<void> logoutAll({RequestOptions? options}) async {
    await client.request(RequestInit(
      method: 'POST',
      path: const ['_auth', 'logout-all'],
      retry: options?.retry,
      headers: options?.headers,
      cache: options?.cache ?? true,
    ));
    await _signedOut(AuthEvent.signedOut);
  }

  /// The signed-in user, with their sign-in methods in [Me.identities].
  Future<Me> me({RequestOptions? options}) async =>
      Me.fromJson((await client.request(RequestInit(
        method: 'GET',
        path: const ['_auth', 'me'],
        retry: options?.retry,
        headers: options?.headers,
        cache: options?.cache ?? true,
      )))
          .data as Map<String, Object?>);

  /// Changes the signed-in user's own settings: today their language, which
  /// must be one the realm lists (case is ignored). Anything else is a
  /// [ValidationError] with code `invalid_locale` and the allowed languages
  /// in `details`.
  Future<User> updateMe({String? locale, RequestOptions? options}) async =>
      User.fromJson((await client.request(RequestInit(
        method: 'PATCH',
        path: const ['_auth', 'me'],
        body: _compact({'locale': locale}),
        retry: options?.retry,
        headers: options?.headers,
        cache: options?.cache ?? true,
      )))
          .data as Map<String, Object?>);

  /// Deletes the signed-in user's account: it is **deactivated** (disabled,
  /// its sessions end) and all data is kept. Erasing a user's data is an
  /// administrator's action — the Dart client has no admin API.
  /// Needs a fresh proof: call [reauth] first (a `reauth_required` error
  /// otherwise).
  Future<void> deleteAccount({RequestOptions? options}) async {
    await client.request(RequestInit(
      method: 'DELETE',
      path: const ['_auth', 'me'],
      reauth: true,
      retry: options?.retry,
      headers: options?.headers,
      cache: options?.cache ?? true,
    ));
    await _signedOut(AuthEvent.signedOut);
  }

  /// Changes the password. This session stays valid; all others end. Needs a
  /// fresh proof: call [reauth] first.
  Future<void> changePassword(
    String newPassword, {
    RequestOptions? options,
  }) =>
      client.request(RequestInit(
        method: 'POST',
        path: const ['_auth', 'password'],
        reauth: true,
        body: {'new_password': newPassword},
        retry: options?.retry,
        headers: options?.headers,
        cache: options?.cache ?? true,
      ));

  /// Proves who the signed-in user is again, before a sensitive action
  /// (changing the password or the address, deleting the account, unlinking a
  /// sign-in method, managing the second factor). `method` is the strongest
  /// proof the user has (see [Me.reauthMethods]): `totp` or `recovery_code`
  /// with a second factor, `password`, or `email_code` after
  /// [requestReauthCode]. The client keeps the proof and sends it with the
  /// sensitive requests while it is good (the realm's `reauth.ttl`, 5 minutes
  /// by default), so one prompt covers a settings page. A wrong proof is an
  /// [AuthenticationError] (`invalid_credentials`) or a [ValidationError]
  /// (`invalid_code`) and counts against the same limits as signing in.
  Future<({String reauthToken, String expiresAt})> reauth(
    String method, {
    String? code,
    String? password,
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'POST',
      path: const ['_auth', 'reauth'],
      body: _compact({'method': method, 'code': code, 'password': password}),
      retry: options?.retry,
      headers: options?.headers,
      cache: options?.cache ?? true,
    ));
    final data = res.data as Map<String, Object?>;
    client.proof = ReauthProof(
      token: data['reauth_token'] as String,
      expires: DateTime.parse(data['expires_at'] as String),
    );
    return (
      reauthToken: data['reauth_token'] as String,
      expiresAt: data['expires_at'] as String,
    );
  }

  /// Whether the client holds a proof that is still good (see [reauth]).
  bool hasFreshProof() =>
      client.proof != null && client.proof!.expires.isAfter(DateTime.now());

  /// Emails a code to a user who has no password and no second factor, to
  /// give to [reauth] with `method: 'email_code'`.
  Future<void> requestReauthCode({RequestOptions? options}) =>
      client.request(RequestInit(
        method: 'POST',
        path: const ['_auth', 'reauth', 'email-code'],
        retry: options?.retry,
        headers: options?.headers,
        cache: options?.cache ?? true,
      ));

  /// The user's active sessions, newest first.
  Future<List<SessionInfo>> sessions({RequestOptions? options}) async {
    final res = await client.request(RequestInit(
      method: 'GET',
      path: const ['_auth', 'sessions'],
      retry: options?.retry,
      headers: options?.headers,
      cache: options?.cache ?? true,
    ));
    return [
      for (final s in (res.data as Map)['items'] as List? ?? const [])
        if (s is Map) SessionInfo.fromJson(Map<String, Object?>.from(s)),
    ];
  }

  /// Ends one of the user's sessions, for example a lost device.
  Future<void> revokeSession(String id, {RequestOptions? options}) =>
      client.request(RequestInit(
        method: 'DELETE',
        path: ['_auth', 'sessions', id],
        retry: options?.retry,
        headers: options?.headers,
        cache: options?.cache ?? true,
      ));

  /// The stored session token, if any.
  Future<String?> token() async => (await client.storage.get());

  /// Whether this client believes it is signed in: it holds a token. An app
  /// that has just started knows nothing more: ask the server with [me].
  Future<bool> hasSession() async => (await client.storage.get()) != null;

  /// Calls [listener] on sign-in, sign-out and session expiry; the returned
  /// function stops it. For the stream form see [events].
  void Function() onAuthChange(void Function(AuthChange) listener) {
    final sub = events.listen(listener);
    return sub.cancel;
  }

  /// Stores the session token, clears what was read as another caller and
  /// emits [AuthEvent.signedIn].
  Future<Session> _signedIn(Session session) async {
    client.clearDerivedState();
    if (session.token != null) await client.storage.set(session.token!);
    current = session;
    await _remember({'org': session.org, 'expires_at': session.expiresAt});
    client.config.credentialChanged();
    _events.add(AuthChange(AuthEvent.signedIn, session));
    return session;
  }

  /// Stores a session this client did not get from a sign-in of its own (the
  /// one creating an organization opens).
  @internal
  Future<Session> signedInWith(Session session) => _signedIn(session);

  /// Keeps a note of the session beside the token of an organization's
  /// client, for [Orgs.signedIn].
  Future<void> _remember(Map<String, Object?> fields) async {
    if (client.orgRef.isEmpty) return;
    final note = client.root.storage.slot('$orgNotePrefix${client.orgRef}');
    if (note == null) return;
    var held = <String, Object?>{};
    try {
      final text = await note.get();
      if (text != null) {
        held = Map<String, Object?>.from(jsonDecode(text) as Map);
      }
    } on Object {
      // A damaged note is replaced.
    }
    await note.set(jsonEncode({
      ...held,
      for (final e in fields.entries)
        if (e.value != null) e.key: e.value,
    }));
  }

  /// Forgets the stored token, clears derived state and emits [event].
  Future<void> _signedOut(AuthEvent event) async {
    client.clearDerivedState();
    await client.storage.remove();
    current = null;
    if (client.orgRef.isNotEmpty) {
      await client.root.storage
          .slot('$orgNotePrefix${client.orgRef}')
          ?.remove();
    }
    client.config.credentialChanged();
    _events.add(AuthChange(event, null));
  }

  /// Called by the client when the server refuses the stored token
  /// (`unauthenticated`).
  FutureOr<void> sessionExpired() => _signedOut(AuthEvent.sessionExpired);
}

/// The signed-in user's second factor: `client.auth.mfa`. Changes need a
/// fresh proof: see [Auth.reauth].
final class Mfa {
  Mfa(this._auth);

  final Auth _auth;

  /// Whether a second factor is available, enabled, which `methods` and how
  /// many recovery codes remain; `required` when the user's role demands one.
  Future<MfaStatus> status({RequestOptions? options}) async =>
      MfaStatus.fromJson((await _auth.client.request(RequestInit(
        method: 'GET',
        path: const ['_auth', 'mfa'],
        retry: options?.retry,
        headers: options?.headers,
        cache: options?.cache ?? true,
      )))
          .data as Map<String, Object?>);

  /// Starts setting an authenticator app up. With the `challenge` of an
  /// [EnrollmentChallenge] there is no session yet.
  Future<MfaSetup> setup({String? challenge, RequestOptions? options}) async =>
      MfaSetup.fromJson((await _auth.client.request(RequestInit(
        method: 'POST',
        path: const ['_auth', 'mfa', 'totp', 'setup'],
        auth: challenge == null,
        reauth: challenge == null,
        body: _compact({'challenge': challenge}),
        retry: options?.retry,
        headers: options?.headers,
        cache: options?.cache ?? true,
      )))
          .data as Map<String, Object?>);

  /// Turns it on with the first code of the app. Resolves with the
  /// `recovery_codes`, shown once; with a `challenge` it also ends the
  /// sign-in and the client holds the session ([MfaConfirmResult.session] is
  /// it), unless the user has consents to accept first: then
  /// [MfaConfirmResult.consent] is the [ConsentChallenge] to finish with
  /// [Auth.acceptConsents].
  Future<MfaConfirmResult> confirm({
    required String setupId,
    required String code,
    String? challenge,
    RequestOptions? options,
  }) async {
    final res = await _auth.client.request(RequestInit(
      method: 'POST',
      path: const ['_auth', 'mfa', 'totp', 'confirm'],
      auth: challenge == null,
      reauth: challenge == null,
      body: {
        'setup_id': setupId,
        'code': code,
        ..._compact({'challenge': challenge}),
      },
      retry: options?.retry,
      headers: options?.headers,
      cache: options?.cache ?? true,
    ));
    final data = res.data as Map<String, Object?>;
    final codes = [
      for (final c in data['recovery_codes'] as List? ?? const []) '$c',
    ];
    if (data['consent_required'] == true) {
      return MfaConfirmResult(
        recoveryCodes: codes,
        consent: ConsentChallenge(
          challenge: data['challenge'] as String,
          consents: [
            for (final c in data['consents'] as List? ?? const [])
              if (c is Map)
                PendingConsent(name: '${c['name']}', version: '${c['version']}'),
          ],
        ),
      );
    }
    if (data['token'] != null || data['session_id'] != null) {
      final session =
          await _auth._signedIn(Session.fromJson(data..remove('recovery_codes')));
      return MfaConfirmResult(recoveryCodes: codes, session: session);
    }
    return MfaConfirmResult(recoveryCodes: codes);
  }

  /// Turns it off (not for a role that requires it: `mfa_required_by_role`).
  Future<void> disable({RequestOptions? options}) =>
      _auth.client.request(RequestInit(
        method: 'DELETE',
        path: const ['_auth', 'mfa', 'totp'],
        reauth: true,
        retry: options?.retry,
        headers: options?.headers,
        cache: options?.cache ?? true,
      ));

  /// Replaces the recovery codes; the new ones are shown once.
  Future<List<String>> regenerateRecoveryCodes({RequestOptions? options}) async {
    final res = await _auth.client.request(RequestInit(
      method: 'POST',
      path: const ['_auth', 'mfa', 'recovery-codes'],
      reauth: true,
      retry: options?.retry,
      headers: options?.headers,
      cache: options?.cache ?? true,
    ));
    return [
      for (final c in (res.data as Map)['recovery_codes'] as List? ?? const [])
        '$c',
    ];
  }
}

/// Sign-in by a link or a code emailed to the user: `client.auth.magicLink`.
final class MagicLink {
  MagicLink(this._auth);

  final Auth _auth;

  /// Asks for a message with a link and a 6-digit code. Always resolves,
  /// whatever the address is. The link ends at `redirectTo`, where
  /// [Auth.completeSignIn] finishes the sign-in; the code is typed in this
  /// app with [verifyCode]. The PKCE verifier waits in [Auth.oauthStorage]
  /// for both.
  ///
  /// `consents` are accepted for the account a new address gets (a new
  /// account made without them is asked at its first sign-in).
  /// `org` is the organization the person signs in to (an id or a slug; the
  /// client's own when it was made with one): only its members are sent a
  /// link, and the session it gives is scoped to it.
  Future<({String requestId})> request(
    String email, {
    String? redirectTo,
    String? locale,
    String? org,
    Map<String, String>? consents,
    RequestOptions? options,
  }) async {
    final (:verifier, :challenge) = newPKCE();
    final res = await _auth.client.request(RequestInit(
      method: 'POST',
      path: [..._auth.base(org), 'magic-link'],
      auth: false,
      body: _compact({
        'email': email,
        'redirect_to': redirectTo,
        'code_challenge': challenge,
        'locale': locale,
        'consents': consents,
      }),
      retry: options?.retry,
      headers: options?.headers,
      cache: options?.cache ?? true,
    ));
    final requestId = (res.data as Map)['request_id'] as String;
    await savePending(
      _auth.oauthStorage,
      verifier: verifier,
      provider: 'magic-link',
      intent: 'signin',
      requestId: requestId,
    );
    return (requestId: requestId);
  }

  /// Signs in with the code of the message, in the app that asked. The answer
  /// is the session or a second step, like [Auth.login]'s. A wrong code is
  /// `invalid_code` (five end the request); a request that is unknown, used
  /// or expired is `invalid_magic_link`.
  Future<SignInResult> verifyCode(String code, {RequestOptions? options}) async {
    final storage = _auth.oauthStorage;
    final pending = await loadPending(storage);
    if (pending?.requestId == null) {
      throw ValidationError(
        status: 400,
        code: 'no_request',
        message:
            'ask for a link first, in this app: magicLink.request(email)',
      );
    }
    final res = await _auth.client.request(RequestInit(
      method: 'POST',
      path: const ['_auth', 'magic-link', 'verify'],
      auth: false,
      body: {
        'request_id': pending!.requestId,
        'code': code.trim(),
        'code_verifier': pending.verifier,
      },
      retry: options?.retry,
      headers: options?.headers,
      cache: options?.cache ?? true,
    ));
    await storage.remove();
    return _auth.step(res.data);
  }
}

/// Setting and removing the password of an account that may sign in another
/// way: `client.auth.password`.
final class PasswordAuth {
  PasswordAuth(this._auth);

  final Auth _auth;

  /// Sets the password; see [Auth.changePassword].
  Future<void> set(String newPassword, {RequestOptions? options}) =>
      _auth.changePassword(newPassword, options: options);

  /// Removes the password; needs a fresh proof (see [Auth.reauth]).
  Future<void> remove({RequestOptions? options}) =>
      _auth.client.request(RequestInit(
        method: 'DELETE',
        path: const ['_auth', 'password'],
        reauth: true,
        retry: options?.retry,
        headers: options?.headers,
        cache: options?.cache ?? true,
      ));
}
