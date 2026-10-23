import 'dart:io' show HttpDate;

import 'package:backd/backd.dart';
import 'package:test/test.dart';

import 'helpers.dart';

void main() {
  group('errorFromResponse status mapping', () {
    test('statuses map to their classes', () {
      for (final (status, type) in [
        (400, ValidationError),
        (401, AuthenticationError),
        (403, ForbiddenError),
        (404, NotFoundError),
        (409, ConflictError),
        (412, VersionMismatchError),
        (429, RetryableError),
        (503, RetryableError),
      ]) {
        final err = errorFromResponse(respond(status, {
          'error': {'code': 'x_$status', 'message': 'm$status'},
        }));
        expect(err, isA<BackdException>().having((e) => e.runtimeType, 'type', type));
        expect(err.status, status);
        expect(err.code, 'x_$status');
      }
      // Unmapped statuses land on the base class.
      expect(errorFromResponse(respond(500)), isA<BackdException>());
      expect(errorFromResponse(respond(418)).status, 418);
    });

    test('missing or non-JSON bodies degrade to http_<status>', () {
      final err = errorFromResponse(respond(502, 'bad gateway'));
      expect(err.code, 'http_502');
      expect(err.status, 502);
    });

    test('details and request_id come from the body', () {
      final err = errorFromResponse(respond(400, {
        'error': {
          'code': 'validation_error',
          'message': 'bad',
          'details': [
            {'path': 'title', 'reason': 'is required'},
          ],
          'request_id': 'req-1',
        },
      }));
      expect(err.details.single.path, 'title');
      expect(err.details.single.reason, 'is required');
      expect(err.requestId, 'req-1');
    });

    test('request_id falls back to the X-Request-ID header', () {
      final err = errorFromResponse(respond(
        400,
        {'error': {'code': 'x', 'message': 'm'}},
        {'X-Request-ID': 'req-2'},
      ));
      expect(err.requestId, 'req-2');
    });
  });

  group('typed errors by code', () {
    BackdException of(String code, [int status = 400]) => errorFromResponse(
          respond(status, {'error': {'code': code, 'message': 'm'}}),
        );

    test('document and collection codes', () {
      expect(of('id_required'), isA<InvalidIdError>());
      expect(of('invalid_id'), isA<InvalidIdError>());
      expect(of('id_not_allowed'), isA<InvalidIdError>());
      expect(of('id_taken', 409), isA<IdTakenError>());
      expect(of('invalid_expand'), isA<InvalidExpandError>());
      expect(of('expand_too_large'), isA<ExpandTooLargeError>());
    });

    test('password_expired tells whether the reset email was queued', () {
      BackdException expired(List<Map<String, Object?>> details) =>
          errorFromResponse(respond(401, {
            'error': {'code': 'password_expired', 'message': 'm', 'details': details},
          }));
      final queued = expired([
        {'path': 'reset_email', 'reason': 'queued'},
      ]);
      expect(queued, isA<PasswordExpiredError>());
      expect(queued, isA<AuthenticationError>());
      expect((queued as PasswordExpiredError).resetEmailQueued, isTrue);
      expect((expired([]) as PasswordExpiredError).resetEmailQueued, isFalse);
    });

    test('field_encrypted is a validation error', () {
      expect(of('field_encrypted'), isA<FieldEncryptedError>());
      expect(of('field_encrypted'), isA<ValidationError>());
    });

    test('organization and team codes', () {
      expect(of('org_requires_mfa', 403), isA<OrgRequiresMFAError>());
      expect(of('org_role_requires_mfa', 403), isA<OrgRoleRequiresMFAError>());
      expect(of('not_team_member', 403), isA<NotTeamMemberError>());
      expect(of('team_required'), isA<TeamError>());
      expect(of('team_not_allowed'), isA<TeamError>());
      expect(of('not_org_member', 403), isA<NotOrgMemberError>());
      expect(of('last_org_manager', 409), isA<LastOrgManagerError>());
    });

    test('aggregate and search codes', () {
      expect(of('invalid_aggregate'), isA<InvalidAggregateError>());
      expect(of('aggregate_not_enabled'), isA<AggregateNotEnabledError>());
      expect(of('aggregate_too_expensive', 422), isA<AggregateTooExpensiveError>());
      expect(of('search_not_enabled'), isA<SearchNotEnabledError>());
      expect(of('search_too_expensive', 422), isA<SearchTooExpensiveError>());
    });

    test('realtime codes', () {
      expect(of('realtime_not_enabled'), isA<RealtimeNotEnabledError>());
      expect(of('too_many_subscriptions'), isA<TooManySubscriptionsError>());
      expect(of('invalid_resume_token'), isA<InvalidResumeTokenError>());
      expect(of('session_ended', 401), isA<AuthenticationError>());
    });

    test('index_building is retryable and parses Retry-After', () {
      final err = errorFromResponse(respond(
        503,
        {'error': {'code': 'index_building', 'message': 'rebuilding'}},
        {'Retry-After': '7'},
      ));
      expect(err, isA<IndexBuildingError>());
      expect(err, isA<RetryableError>());
      expect((err as IndexBuildingError).retryAfter, const Duration(seconds: 7));
    });

    test('consent_required lists the pending consents', () {
      final err = errorFromResponse(respond(400, {
        'error': {
          'code': 'consent_required',
          'message': 'accept the new terms',
          'details': [
            {'path': 'consents.terms', 'reason': 'required, version v3'},
            {'path': 'consents.privacy', 'reason': 'outdated, version v9'},
            {'path': 'email', 'reason': 'is required'},
          ],
        },
      }));
      expect(err, isA<ConsentRequiredError>());
      final consents = (err as ConsentRequiredError).consents;
      expect(consents.map((c) => c.name), ['terms', 'privacy']);
      expect(consents.map((c) => c.version), ['v3', 'v9']);
    });
  });

  group('parseRetryAfter', () {
    test('seconds', () {
      expect(parseRetryAfter('12'), const Duration(seconds: 12));
      expect(parseRetryAfter('0'), Duration.zero);
    });

    test('HTTP date', () {
      final inTenSeconds = HttpDate.format(
        DateTime.now().add(const Duration(seconds: 10)),
      );
      final parsed = parseRetryAfter(inTenSeconds);
      expect(parsed, isNotNull);
      expect(parsed!.inMilliseconds, greaterThan(0));
      expect(parsed.inMilliseconds, lessThanOrEqualTo(10000));
    });

    test('garbage and absent', () {
      expect(parseRetryAfter('not a date'), isNull);
      expect(parseRetryAfter(''), isNull);
      expect(parseRetryAfter(null), isNull);
    });
  });

  test('NetworkError carries the cause', () {
    final cause = StateError('socket gone');
    final err = NetworkError(
      status: 0,
      code: 'network_error',
      message: 'network error: $cause',
      cause: cause,
    );
    expect(err.cause, same(cause));
  });
}
