import 'dart:async';
import 'dart:convert';
import 'dart:math';

import 'package:crypto/crypto.dart';

import 'errors.dart';

/// Where the PKCE verifier waits while the user is at the provider: it must
/// survive the browser round trip, so keep it in something that outlives the
/// method call (a file, secure storage) when the app itself leaves.
abstract interface class OAuthStorage {
  /// The stored pending value, or null.
  FutureOr<String?> get();

  /// Stores [value].
  FutureOr<void> set(String value);

  /// Forgets the pending value.
  FutureOr<void> remove();
}

/// The default [OAuthStorage]: memory. Enough for an app that handles the
/// redirect without leaving — a desktop app that opens the system browser is
/// still running when it receives the deep link or loopback call.
final class MemoryOAuthStorage implements OAuthStorage {
  String? _held;

  @override
  Future<String?> get() async => _held;

  @override
  Future<void> set(String value) async => _held = value;

  @override
  Future<void> remove() async => _held = null;
}

/// What backd adds to the address it sends the browser back to is prefixed,
/// so it can't collide with the app's own parameters.
const _paramPrefix = 'backd_';

/// The server's state lives ten minutes; older attempts are dropped.
const _pendingLife = Duration(minutes: 10);

final _secureRandom = Random.secure();

/// The S256 challenge of a verifier.
String challengeOf(String verifier) =>
    base64UrlEncode(sha256.convert(utf8.encode(verifier)).bytes)
        .replaceAll('=', '');

/// A PKCE verifier (256 random bits) and its S256 challenge.
({String verifier, String challenge}) newPKCE() {
  final bytes = List<int>.generate(32, (_) => _secureRandom.nextInt(256));
  final verifier = base64UrlEncode(bytes).replaceAll('=', '');
  return (verifier: verifier, challenge: challengeOf(verifier));
}

/// An OAuth attempt this app started and is waiting to finish.
final class Pending {
  const Pending({
    required this.verifier,
    required this.provider,
    required this.intent,
    this.requestId,
    required this.at,
  });

  final String verifier;
  final String provider;

  /// `signin` or `link`.
  final String intent;

  /// A magic-link request waiting for its code.
  final String? requestId;

  /// When the attempt started (milliseconds since the epoch).
  final int at;
}

/// Stores an attempt [Pending] in [storage].
FutureOr<void> savePending(
  OAuthStorage storage, {
  required String verifier,
  required String provider,
  required String intent,
  String? requestId,
}) =>
    storage.set(jsonEncode({
      'verifier': verifier,
      'provider': provider,
      'intent': intent,
      if (requestId != null) 'request_id': requestId,
      'at': DateTime.now().millisecondsSinceEpoch,
    }));

/// The attempt this app started, or null when none is waiting (or it is too
/// old to work).
Future<Pending?> loadPending(OAuthStorage storage) async {
  try {
    final raw = await storage.get();
    final p = raw == null ? null : jsonDecode(raw);
    if (p is Map && p['verifier'] is String && p['at'] is int) {
      final at = p['at'] as int;
      if (DateTime.now().millisecondsSinceEpoch - at < _pendingLife.inMilliseconds) {
        return Pending(
          verifier: p['verifier'] as String,
          provider: p['provider'] as String? ?? '',
          intent: p['intent'] as String? ?? 'signin',
          requestId: p['request_id'] as String?,
          at: at,
        );
      }
    }
  } on FormatException {
    // Unreadable: as if there were none.
  }
  return null;
}

/// What backd put in the address it sent the browser back to.
final class OAuthReturn {
  const OAuthReturn({this.code, this.error, this.linked, this.provider});

  /// A one-time login code.
  final String? code;

  /// Why the sign-in didn't work.
  final String? error;

  /// The provider that was linked to the signed-in user.
  final String? linked;

  /// With `account_exists`: the provider that was tried.
  final String? provider;
}

/// Reads the outcome of the redirect flow from the URI the app received (a
/// deep link or the loopback listener's callback); null when it carries none.
OAuthReturn? parseReturn(Uri uri) {
  final q = uri.queryParameters;
  final out = OAuthReturn(
    code: q['${_paramPrefix}code'],
    error: q['${_paramPrefix}error'],
    linked: q['${_paramPrefix}linked'],
    provider: q['${_paramPrefix}provider'],
  );
  return out.code != null || out.error != null || out.linked != null
      ? out
      : null;
}

/// The error for a `backd_error` code of the redirect flow, as the same
/// classes the rest of the client throws: `account_exists` and
/// `link_conflict` are a [ConflictError], `signup_closed`,
/// `email_not_verified` and `signin_refused` a [ForbiddenError], `cancelled`
/// an [AuthenticationError], and so on; `code` is the error code, and
/// `details` names the provider.
BackdException oauthError(String code, [String? provider]) {
  final known = _oauthErrors[code] ?? _oauthErrors['provider_error']!;
  return errorForStatus(
    known.status,
    code: _oauthErrors.containsKey(code) ? code : 'provider_error',
    message: known.message,
    details: provider == null
        ? const []
        : [ErrorDetail(path: 'provider', reason: provider)],
  );
}

const _oauthErrors = <String, ({int status, String message})>{
  'cancelled': (status: 401, message: 'the sign-in was cancelled at the provider'),
  'account_exists': (status: 409, message: 'an account with that address exists: sign in another way and link this provider from there'),
  'signup_closed': (status: 403, message: 'sign-up is not open in this realm'),
  'invitation_invalid': (status: 400, message: 'sign-up needs a valid invitation'),
  'link_conflict': (status: 409, message: 'that provider account can not be linked to this user'),
  'email_not_verified': (status: 403, message: 'the address of this account is not verified'),
  'email_required': (status: 400, message: 'the provider gave no email address'),
  'signin_refused': (status: 403, message: 'this account can not sign in'),
  'provider_error': (status: 502, message: 'the provider failed or answered something backd can not accept'),
};
