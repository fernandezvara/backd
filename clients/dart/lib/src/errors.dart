import 'dart:convert';

import 'transport.dart';

/// One detail of an error answer: which field or parameter, and what's wrong.
final class ErrorDetail {
  const ErrorDetail({required this.path, required this.reason});

  factory ErrorDetail.fromJson(Object? json) {
    final map = json is Map ? json : const <Object?, Object?>{};
    return ErrorDetail(
      path: '${map['path'] ?? ''}',
      reason: '${map['reason'] ?? ''}',
    );
  }

  /// Field or parameter the problem is about.
  final String path;

  /// What's wrong with it.
  final String reason;
}

/// A consent the realm still needs: `{ name, version }`. Show the texts with
/// `client.consents.text` and send the acceptance with `client.consents.accept`.
final class PendingConsent {
  const PendingConsent({required this.name, required this.version});

  final String name;
  final String version;
}

/// An error answered by backd, or a network failure (status 0).
/// [code] is stable and meant for programs; [message] is for humans.
class BackdException implements Exception {
  const BackdException({
    required this.status,
    required this.code,
    required this.message,
    this.details = const [],
    this.requestId,
    this.cause,
  });

  /// HTTP status; 0 for network failures.
  final int status;

  /// backd error code, e.g. `validation_error`.
  final String code;

  final String message;
  final List<ErrorDetail> details;

  /// The request ID, to match server logs.
  final String? requestId;

  /// What caused this, for a [NetworkError] the thrown object.
  final Object? cause;

  @override
  String toString() => '$runtimeType $status $code: $message';
}

/// 400: invalid body, query or header (`validation_error`, `invalid_json`,
/// `invalid_query`, `invalid_header`).
class ValidationError extends BackdException {
  const ValidationError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// 401: missing, invalid or expired credentials, or a wrong email or password.
class AuthenticationError extends BackdException {
  const AuthenticationError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// 401 `password_expired`: the password is right, but an administrator
/// expired it, so it opens nothing until the user chooses a new one. Only the
/// right password gets this answer (a wrong one is `invalid_credentials`).
///
/// [resetEmailQueued] says that backd queued the email with the link to choose
/// a new password, so the app can say "we sent you an email". When it is false
/// (the realm sends no email, or the limits held it back) offer
/// `auth.requestPasswordReset`, or tell the user to ask their administrator.
/// A signed-in user's session is not ended by this.
final class PasswordExpiredError extends AuthenticationError {
  PasswordExpiredError({
    required super.status,
    required super.code,
    required super.message,
    super.details = const [],
    super.requestId,
    super.cause,
  }) : resetEmailQueued = details.any((d) => d.path == 'reset_email');

  final bool resetEmailQueued;
}

/// 400 `realtime_not_enabled`: the collection doesn't declare `realtime: true`
/// (or lost it).
final class RealtimeNotEnabledError extends ValidationError {
  const RealtimeNotEnabledError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// 400 `too_many_subscriptions`: a stream may hold only so many subscriptions.
final class TooManySubscriptionsError extends ValidationError {
  const TooManySubscriptionsError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// 400 `invalid_resume_token`: a resume token is not valid for this caller;
/// the client drops it and reloads.
final class InvalidResumeTokenError extends ValidationError {
  const InvalidResumeTokenError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// The stream's session, user or key is gone (the server said `session_ended`).
/// Live views stop and report this; sign in again and subscribe again.
final class SessionEndedError extends AuthenticationError {
  const SessionEndedError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// 403: the caller isn't allowed to do this.
class ForbiddenError extends BackdException {
  const ForbiddenError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// Not an HTTP error: a sign-up in a realm that requires verified addresses
/// answers `202`, because the account exists but there is no session. Thrown
/// by `auth.signup()` so the code after it, which expects a session, doesn't run.
final class VerificationRequiredError extends BackdException {
  const VerificationRequiredError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// Not an HTTP error: a password login (or sign-up) that needs a second step
/// before it gives a session. `auth.login()` and `auth.signup()` promise a
/// session, so they throw this instead; `auth.signIn()` returns the step as a
/// value. `code` is `mfa_required` (finish with
/// `auth.verifyMfa(challenge, method, code)`; [methods] says which),
/// `mfa_enrollment_required` (set a second factor up with
/// `auth.mfa.setup(challenge)`) or `consent_required` (accept [consents] with
/// `auth.acceptConsents(challenge, consents)`).
final class SecondStepRequiredError extends BackdException {
  const SecondStepRequiredError({
    required super.status,
    required super.code,
    required super.message,
    required this.challenge,
    this.methods = const [],
    this.consents = const [],
    super.details,
    super.requestId,
    super.cause,
  });

  final String challenge;
  final List<String> methods;

  /// With `code: 'consent_required'`: the consents to accept.
  final List<PendingConsent> consents;
}

/// `consent_required`: consents have to be accepted. At sign-up (`400`) a
/// required consent is missing or outdated; on a request of a signed-in user
/// (`403`) a version's `reaccept_by` date has passed. [consents] lists what to
/// accept.
final class ConsentRequiredError extends BackdException {
  ConsentRequiredError({
    required super.status,
    required super.code,
    required super.message,
    super.details = const [],
    super.requestId,
    super.cause,
  }) : consents = [
          for (final d in details)
            if (_consentName.hasMatch(d.path))
              PendingConsent(
                name: _consentName.firstMatch(d.path)![1]!,
                version: _consentVersion.firstMatch(d.reason)?[1] ?? '',
              ),
        ].where((c) => c.version.isNotEmpty).toList();

  static final _consentName = RegExp(r'^consents\.(.+)$');
  static final _consentVersion = RegExp(r'version (\S+)$');

  final List<PendingConsent> consents;
}

/// 400 `id_required`, `invalid_id` or `id_not_allowed`: the `id` of a create
/// is missing, isn't acceptable ([code] says which; the [details] say why) or
/// was sent to a collection that generates its ids.
final class InvalidIdError extends ValidationError {
  const InvalidIdError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// 403 `org_requires_mfa`: the organization requires a second factor and this
/// session used none. Ask for one (see `mfa_met` of the organization in `client.auth.me()`);
/// leaving the organization still works.
final class OrgRequiresMFAError extends ForbiddenError {
  const OrgRequiresMFAError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// 403 `org_role_requires_mfa`: the action needs a role that requires a
/// second factor and this session used none. [details] names the `role`
/// (and the `team`, for a team role): start the enrollment, or sign in again
/// with a second factor.
final class OrgRoleRequiresMFAError extends ForbiddenError {
  const OrgRoleRequiresMFAError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// 403 `not_team_member`: the create names a team the caller doesn't belong
/// to and holds no team role in (the same answer for an unknown team and one
/// of another organization).
final class NotTeamMemberError extends ForbiddenError {
  const NotTeamMemberError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// 400 `team_required` or `team_not_allowed`: the create of a collection with
/// a required team names none, or names one in a collection that has no teams
/// ([code] says which).
final class TeamError extends ValidationError {
  const TeamError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// 400 `invalid_expand`: the `expand` of a read names a reference the
/// collection doesn't declare, a field the target doesn't have, goes too
/// deep, has too many entries or isn't well formed. The [details] name the
/// part.
final class InvalidExpandError extends ValidationError {
  const InvalidExpandError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// 400 `field_encrypted`: a `where` or `orderBy` names an encrypted field, or
/// asks more of one than equality (`=` and `in`, on a field with
/// `search: equality`). The [details] name the field.
final class FieldEncryptedError extends ValidationError {
  const FieldEncryptedError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// 400 `expand_too_large`: the expansion would return more documents than the
/// server allows (`BACKD_EXPAND_MAX_DOCS`); nothing partial is returned. Ask
/// for fewer references, a smaller `limit` or fewer documents.
final class ExpandTooLargeError extends ValidationError {
  const ExpandTooLargeError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// 400 `invalid_aggregate`: an aggregation names a field the collection
/// doesn't declare, a bad bucket, metric, `orderBy`, `limit` or `tz`. The
/// [details] name each parameter.
final class InvalidAggregateError extends ValidationError {
  const InvalidAggregateError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// 400 `aggregate_not_enabled`: the collection declares no `aggregate:` in
/// its collection.yaml.
final class AggregateNotEnabledError extends ValidationError {
  const AggregateNotEnabledError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// 422 `aggregate_too_expensive`: the aggregation needed more time, memory or
/// distinct values than allowed. Narrow `where`, group by fewer fields, or
/// ask for an index that starts with a `group_by` field.
final class AggregateTooExpensiveError extends BackdException {
  const AggregateTooExpensiveError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// 400 `search_not_enabled`: a `$search` in a collection that declares no
/// text index.
final class SearchNotEnabledError extends ValidationError {
  const SearchNotEnabledError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// 403 `not_org_member`: not a member of the organization, or there is no
/// such organization.
final class NotOrgMemberError extends ForbiddenError {
  const NotOrgMemberError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// 404: unknown realm, collection, document, user or session.
class NotFoundError extends BackdException {
  const NotFoundError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// 409: a unique value already exists (`conflict`, `email_taken`) or
/// concurrent writes (`write_conflict`).
class ConflictError extends BackdException {
  const ConflictError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// 409 `id_taken`: a document with the id of a create exists. Nothing was
/// overwritten.
final class IdTakenError extends ConflictError {
  const IdTakenError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// 409 `last_org_manager`: the organization needs at least one member who can
/// manage it.
final class LastOrgManagerError extends ConflictError {
  const LastOrgManagerError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// 412: `If-Match` didn't match the current version.
final class VersionMismatchError extends BackdException {
  const VersionMismatchError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// 429 and 503: try again later; [retryAfter] is how long when the server said.
class RetryableError extends BackdException {
  const RetryableError({
    required super.status,
    required super.code,
    required super.message,
    this.retryAfter,
    super.details,
    super.requestId,
    super.cause,
  });

  final Duration? retryAfter;
}

/// 422 `search_too_expensive`: a search needed more time than allowed; use
/// fewer or rarer words, or narrow `where`.
final class SearchTooExpensiveError extends BackdException {
  const SearchTooExpensiveError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// 503 `index_building`: a search while the collection's text index is being
/// rebuilt. It is a [RetryableError]: the client retries it after
/// `Retry-After`, and [RetryableError.retryAfter] says how long.
final class IndexBuildingError extends RetryableError {
  const IndexBuildingError({
    required super.status,
    required super.code,
    required super.message,
    super.retryAfter,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// 0: the request didn't get an answer (network error, abort, timeout).
final class NetworkError extends BackdException {
  const NetworkError({
    required super.status,
    required super.code,
    required super.message,
    super.details,
    super.requestId,
    super.cause,
  });
}

/// Builds the error for a non-2xx [response].
BackdException errorFromResponse(TransportResponse response) {
  Object? parsed;
  try {
    final text = response.bodyText;
    if (text.isNotEmpty) parsed = jsonDecode(text);
  } on FormatException {
    // Not JSON (for example from a proxy): keep the defaults below.
  }
  final error = parsed is Map && parsed['error'] is Map
      ? Map<String, Object?>.from(parsed['error'] as Map)
      : const <String, Object?>{};
  final status = response.status;
  final code = error['code'] as String? ?? 'http_$status';
  final message = error['message'] as String? ??
      response.reasonPhrase ??
      'request failed';
  final details = [
    for (final d in error['details'] as List? ?? const [])
      ErrorDetail.fromJson(d),
  ];
  final requestId =
      error['request_id'] as String? ?? response.header('x-request-id');
  final retryAfter = parseRetryAfter(response.header('retry-after'));
  return errorForStatus(
    status,
    code: code,
    message: message,
    details: details,
    requestId: requestId,
    retryAfter: retryAfter,
  );
}

/// The [BackdException] subclass for a status and code, built directly — for
/// answers that never went through a response of their own (OAuth redirect
/// outcomes, async job results).
BackdException errorForStatus(
  int status, {
  required String code,
  required String message,
  List<ErrorDetail> details = const [],
  String? requestId,
  Duration? retryAfter,
}) {
  switch (code) {
    case 'consent_required':
      return ConsentRequiredError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      );
    case 'password_expired':
      return PasswordExpiredError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      );
    case 'field_encrypted':
      return FieldEncryptedError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      );
    case 'realtime_not_enabled':
      return RealtimeNotEnabledError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      );
    case 'too_many_subscriptions':
      return TooManySubscriptionsError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      );
    case 'invalid_resume_token':
      return InvalidResumeTokenError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      );
    case 'id_required':
    case 'invalid_id':
    case 'id_not_allowed':
      return InvalidIdError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      );
    case 'id_taken':
      return IdTakenError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      );
    case 'org_requires_mfa':
      return OrgRequiresMFAError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      );
    case 'org_role_requires_mfa':
      return OrgRoleRequiresMFAError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      );
    case 'not_team_member':
      return NotTeamMemberError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      );
    case 'team_required':
    case 'team_not_allowed':
      return TeamError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      );
    case 'not_org_member':
      return NotOrgMemberError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      );
    case 'last_org_manager':
      return LastOrgManagerError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      );
    case 'invalid_aggregate':
      return InvalidAggregateError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      );
    case 'aggregate_not_enabled':
      return AggregateNotEnabledError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      );
    case 'aggregate_too_expensive':
      return AggregateTooExpensiveError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      );
    case 'search_not_enabled':
      return SearchNotEnabledError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      );
    case 'search_too_expensive':
      return SearchTooExpensiveError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      );
    case 'index_building':
      return IndexBuildingError(
        status: status,
        code: code,
        message: message,
        retryAfter: retryAfter,
        details: details,
        requestId: requestId,
      );
    case 'invalid_expand':
      return InvalidExpandError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      );
    case 'expand_too_large':
      return ExpandTooLargeError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      );
  }
  return switch (status) {
    400 => ValidationError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      ),
    401 => AuthenticationError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      ),
    403 => ForbiddenError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      ),
    404 => NotFoundError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      ),
    409 => ConflictError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      ),
    412 => VersionMismatchError(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      ),
    429 || 503 => RetryableError(
        status: status,
        code: code,
        message: message,
        retryAfter: retryAfter,
        details: details,
        requestId: requestId,
      ),
    _ => BackdException(
        status: status,
        code: code,
        message: message,
        details: details,
        requestId: requestId,
      ),
  };
}
