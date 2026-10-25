import 'dart:convert';

import 'package:backd/backd.dart';
import 'package:test/test.dart';

import 'helpers.dart';

Map<String, Object?> sessionJson({String token = 'bds_t1', String id = 'u1'}) => {
      'token': token,
      'token_type': 'Bearer',
      'session_id': 's1',
      'expires_at': '2030-01-01T00:00:00Z',
      'user': {
        'id': id,
        'email': 'a@b.c',
        'email_verified': true,
        'roles': <String>[],
        'locale': 'en',
        'created_at': '2026-01-01T00:00:00Z',
      },
    };

void main() {
  group('signup and login', () {
    test('signup posts the compact body and stores the token', () async {
      final storage = MemoryTokenStorage();
      final t = FakeTransport()..answer(respond(200, sessionJson()));
      final client = testClient(t, storage: storage);
      final session = await client.auth
          .signup('a@b.c', 'pw', locale: 'es', redirectTo: 'myapp://done');
      final body = jsonDecode(utf8.decode(t.requests.single.body!)) as Map;
      expect(body['email'], 'a@b.c');
      expect(body['password'], 'pw');
      expect(body['locale'], 'es');
      expect(body['redirect_to'], 'myapp://done');
      expect(body.containsKey('invitation'), isFalse);
      expect(headerOf(t.requests.single, 'Authorization'), isNull);
      expect(session.token, 'bds_t1');
      expect(await storage.get(), 'bds_t1');
    });

    test('signup 202 throws VerificationRequiredError', () async {
      final t = FakeTransport()..answer(respond(202, {}));
      expect(
        testClient(t).auth.signup('a@b.c', 'pw'),
        throwsA(isA<VerificationRequiredError>()),
      );
    });

    test('login resolves the session; signIn returns the step', () async {
      final t = FakeTransport()
        ..answer(respond(200, sessionJson()))
        ..answer(respond(200, {
          'mfa_required': true,
          'challenge': 'ch-1',
          'methods': ['totp'],
        }));
      final client = testClient(t);
      expect(await client.auth.login('a@b.c', 'pw'), isA<Session>());
      final step = await client.auth.signIn('a@b.c', 'pw');
      expect(step, isA<MfaChallenge>());
      expect((step as MfaChallenge).challenge, 'ch-1');
      expect(step.methods, ['totp']);
    });

    test('login on a second step throws SecondStepRequiredError', () async {
      final t = FakeTransport()
        ..answer(respond(200, {'mfa_required': true, 'challenge': 'ch-1', 'methods': ['totp']}))
        ..answer(respond(200, {'mfa_enrollment_required': true, 'challenge': 'ch-2'}))
        ..answer(respond(200, {
          'consent_required': true,
          'challenge': 'ch-3',
          'consents': [
            {'name': 'terms', 'version': 'v1'},
          ],
        }));
      final client = testClient(t);
      await expectLater(
        client.auth.login('a@b.c', 'pw'),
        throwsA(isA<SecondStepRequiredError>()
            .having((e) => e.code, 'code', 'mfa_required')),
      );
      await expectLater(
        client.auth.login('a@b.c', 'pw'),
        throwsA(isA<SecondStepRequiredError>()
            .having((e) => e.code, 'code', 'mfa_enrollment_required')),
      );
      await expectLater(
        client.auth.login('a@b.c', 'pw'),
        throwsA(isA<SecondStepRequiredError>()
            .having((e) => e.code, 'code', 'consent_required')
            .having((e) => e.consents.single.name, 'consents', 'terms')),
      );
    });
  });

  group('session lifecycle', () {
    test('events stream reports sign-in, sign-out and expiry', () async {
      final storage = MemoryTokenStorage();
      final t = FakeTransport()
        ..answer(respond(200, sessionJson()))
        ..answer(respond(401, {'error': {'code': 'unauthenticated', 'message': 'x'}}))
        ..answer(respond(204));
      final client = testClient(t, storage: storage);
      final seen = <AuthEvent>[];
      final sub = client.auth.events.listen((c) => seen.add(c.event));
      await client.auth.login('a@b.c', 'pw');
      await expectLater(
        client.request(const RequestInit(method: 'GET', path: ['a', 'b'])),
        throwsA(isA<AuthenticationError>()),
      );
      await client.auth.logout();
      await Future<void>.delayed(Duration.zero);
      expect(seen, [
        AuthEvent.signedIn,
        AuthEvent.sessionExpired,
        AuthEvent.signedOut,
      ]);
      expect(await storage.get(), isNull);
      await sub.cancel();
    });

    test('a session that belongs to no organization expires like a refused one',
        () async {
      final storage = MemoryTokenStorage();
      await storage.set('tok');
      final t = FakeTransport()
        ..answer(respond(401, {'error': {'code': 'session_unscoped', 'message': 'x'}}));
      final client = testClient(t, storage: storage);
      final seen = <AuthEvent>[];
      final sub = client.auth.events.listen((c) => seen.add(c.event));
      await expectLater(
        client.request(const RequestInit(method: 'GET', path: ['a', 'b'])),
        throwsA(isA<AuthenticationError>()),
      );
      await Future<void>.delayed(Duration.zero);
      expect(seen, [AuthEvent.sessionExpired]);
      expect(await storage.get(), isNull);
      await sub.cancel();
    });

    test('logout removes the token even when the request fails', () async {
      final storage = MemoryTokenStorage();
      await storage.set('tok');
      final t = FakeTransport()..fail(StateError('net down'));
      await expectLater(testClient(t, storage: storage).auth.logout(),
          throwsA(isA<NetworkError>()));
      expect(await storage.get(), isNull);
    });

    test('logout swallows a refused session', () async {
      final storage = MemoryTokenStorage();
      await storage.set('tok');
      final t = FakeTransport()..answer(respond(401, {
          'error': {'code': 'unauthenticated', 'message': 'x'},
        }));
      await testClient(t, storage: storage).auth.logout();
      expect(await storage.get(), isNull);
    });

    test('token and hasSession read the storage', () async {
      final storage = MemoryTokenStorage();
      final client = testClient(FakeTransport(), storage: storage);
      expect(await client.auth.hasSession(), isFalse);
      await storage.set('bds_x');
      expect(await client.auth.token(), 'bds_x');
      expect(await client.auth.hasSession(), isTrue);
    });
  });

  group('mfa', () {
    test('status, setup and confirm', () async {
      final t = FakeTransport()
        ..answer(respond(200, {
          'available': true,
          'enabled': false,
          'methods': ['totp'],
          'recovery_codes_left': 0,
          'required': false,
        }))
        ..answer(respond(200, {
          'setup_id': 'su1',
          'secret': 'ABC',
          'otpauth_uri': 'otpauth://x',
          'qr': 'data:image/png;base64,x',
          'expires_at': '2030-01-01T00:00:00Z',
        }))
        ..answer(respond(200, {...sessionJson(token: 'bds_mfa'), 'recovery_codes': ['rc1']}));
      final client = testClient(t);
      final status = await client.auth.mfa.status();
      expect(status.methods, ['totp']);
      final setup = await client.auth.mfa.setup(challenge: 'ch-9');
      expect(setup.setupId, 'su1');
      // With a challenge there is no session to send.
      expect(headerOf(t.requests[1], 'Authorization'), isNull);
      final confirm =
          await client.auth.mfa.confirm(setupId: 'su1', code: '123456', challenge: 'ch-9');
      expect(confirm.recoveryCodes, ['rc1']);
      expect(confirm.session!.token, 'bds_mfa');
    });

    test('verifyMfa finishes the challenge and stores the session', () async {
      final storage = MemoryTokenStorage();
      final t = FakeTransport()
        ..answer(respond(200, {...sessionJson(token: 'bds_done'), 'recovery_codes_left': 3}));
      final client = testClient(t, storage: storage);
      final session = await client.auth.verifyMfa('ch-1', 'totp', '654321');
      expect(session.token, 'bds_done');
      expect(session.recoveryCodesLeft, 3);
      expect(await storage.get(), 'bds_done');
      final body = jsonDecode(utf8.decode(t.requests.single.body!)) as Map;
      expect(body, {'challenge': 'ch-1', 'method': 'totp', 'code': '654321'});
    });
  });

  group('reauth', () {
    test('stores the proof and sensitive requests send it', () async {
      final storage = MemoryTokenStorage();
      await storage.set('tok');
      final t = FakeTransport()
        ..answer(respond(200, {
          'reauth_token': 'rt-1',
          'expires_at': DateTime.now()
              .add(const Duration(minutes: 5))
              .toIso8601String(),
        }))
        ..answer(respond(204));
      final client = testClient(t, storage: storage);
      await client.auth.reauth('password', password: 'pw');
      expect(client.auth.hasFreshProof(), isTrue);
      await client.auth.changePassword('newpw');
      expect(headerOf(t.requests[1], 'X-Backd-Reauth'), 'rt-1');
    });

    test('withProof retries once after reauth_required', () async {
      final storage = MemoryTokenStorage();
      await storage.set('tok');
      final t = FakeTransport()
        ..answer(respond(401, {'error': {'code': 'reauth_required', 'message': 'x'}}))
        ..answer(respond(200, meJson(reauthMethods: ['password'])))
        ..answer(respond(200, {
          'reauth_token': 'rt-9',
          'expires_at': DateTime.now()
              .add(const Duration(minutes: 5))
              .toIso8601String(),
        }))
        ..answer(respond(204));
      final client = testClient(t, storage: storage);
      var asks = 0;
      await client.auth.withProof((methods) async {
        asks++;
        expect(methods, ['password']);
        return (method: 'password', code: null, password: 'pw');
      }, () async {
        await client.auth.deleteAccount();
      });
      expect(asks, 1);
      expect(t.requests, hasLength(4)); // delete, me, reauth, delete again
    });
  });

  group('oauth', () {
    test('signInWith stores a pending verifier and returns the URL', () async {
      final t = FakeTransport()
        ..answer(respond(200, {'authorize_url': 'https://accounts.example.com/auth?x=1'}));
      final client = testClient(t);
      Uri? navigated;
      final out = await client.auth.signInWith('google',
          redirectTo: 'myapp://cb',
          navigate: (u) => navigated = u);
      expect(out.authorizeUrl, 'https://accounts.example.com/auth?x=1');
      expect(navigated.toString(), out.authorizeUrl);
      final body = jsonDecode(utf8.decode(t.requests.single.body!)) as Map;
      expect(body['intent'], 'signin');
      expect(body['code_challenge'], isA<String>());
      expect(body['redirect_to'], 'myapp://cb');
      expect(headerOf(t.requests.single, 'Authorization'), isNull);
    });

    test('completeSignIn redeems the code into a session', () async {
      final t = FakeTransport()
        ..answer(respond(200, {'authorize_url': 'https://p.example.com'}))
        ..answer(respond(200, {...sessionJson(token: 'bds_oauth'), 'new_user': true}));
      final client = testClient(t);
      await client.auth.signInWith('google', redirectTo: 'myapp://cb');
      final res = await client
          .auth
          .completeSignIn(Uri.parse('myapp://cb?backd_code=c-42'));
      expect(res, isA<Session>());
      expect((res as Session).newUser, isTrue);
      final body = jsonDecode(utf8.decode(t.requests[1].body!)) as Map;
      expect(body['code'], 'c-42');
      expect(body['code_verifier'], isA<String>());
    });

    test('completeSignIn: errors, links, ordinary visits, wrong device', () async {
      final t = FakeTransport()
        ..answer(respond(200, {'authorize_url': 'https://p.example.com'}));
      final client = testClient(t);
      // Ordinary visit.
      expect(await client.auth.completeSignIn(Uri.parse('myapp://cb')), isNull);
      // An error in the address becomes the matching exception.
      await expectLater(
        client.auth
            .completeSignIn(Uri.parse('myapp://cb?backd_error=account_exists')),
        throwsA(isA<ConflictError>()),
      );
      // A code with no pending attempt is wrong_device.
      await expectLater(
        client.auth.completeSignIn(Uri.parse('myapp://cb?backd_code=x')),
        throwsA(isA<ValidationError>().having((e) => e.code, 'code', 'wrong_device')),
      );
      // A pending link resolves with the provider name.
      await client.auth.linkProvider('google', redirectTo: 'myapp://cb');
      final linked = await client
          .auth
          .completeSignIn(Uri.parse('myapp://cb?backd_linked=google'));
      expect(linked, isA<LinkedResult>());
      // The same outcome again with no URI.
      expect(await client.auth.completeSignIn(), isA<LinkedResult>());
    });
  });

  group('magic link', () {
    test('request + verifyCode sign in', () async {
      final t = FakeTransport()
        ..answer(respond(200, {'request_id': 'rq-1'}))
        ..answer(respond(200, sessionJson(token: 'bds_magic')));
      final client = testClient(t);
      final req = await client.auth.magicLink.request('a@b.c', redirectTo: 'myapp://ml');
      expect(req.requestId, 'rq-1');
      final res = await client.auth.magicLink.verifyCode(' 123456 ');
      expect(res, isA<Session>());
      final body = jsonDecode(utf8.decode(t.requests[1].body!)) as Map;
      expect(body['request_id'], 'rq-1');
      expect(body['code'], '123456');
      expect(body['code_verifier'], isA<String>());
    });

    test('verifyCode without a request is no_request', () async {
      expect(
        testClient(FakeTransport()).auth.magicLink.verifyCode('123456'),
        throwsA(isA<ValidationError>().having((e) => e.code, 'code', 'no_request')),
      );
    });
  });

  group('account and sessions', () {
    test('me, updateMe, sessions, revokeSession', () async {
      final t = FakeTransport()
        ..answer(respond(200, meJson()))
        ..answer(respond(200, {...sessionJson()['user'] as Map, 'locale': 'es'}))
        ..answer(respond(200, {
          'items': [
            {'id': 's1', 'created_at': 'x', 'last_used_at': 'y', 'expires_at': 'z', 'current': true},
            {'id': 's2', 'created_at': 'x', 'last_used_at': 'y', 'expires_at': 'z', 'current': false},
          ],
        }))
        ..answer(respond(204));
      final client = testClient(t, apiKey: 'k');
      final me = await client.auth.me();
      expect(me.identities.single.provider, 'password');
      expect(me.reauthMethods, ['password']);
      final user = await client.auth.updateMe(locale: 'es');
      expect(user.locale, 'es');
      final sessions = await client.auth.sessions();
      expect(sessions, hasLength(2));
      expect(sessions[0].current, isTrue);
      await client.auth.revokeSession('s2');
      expect(t.requests[3].url.path, '/v1/shelf/_auth/sessions/s2');
      expect(t.requests[3].method, 'DELETE');
    });

    test('email/reset/invitation flows hit the right endpoints', () async {
      final t = FakeTransport()
        ..answer(respond(202))
        ..answer(respond(202))
        ..answer(respond(202))
        ..answer(respond(202))
        ..answer(respond(202))
        ..answer(respond(202))
        ..answer(respond(202));
      final client = testClient(t);
      await client.auth.resendVerification('a@b.c');
      await client.auth.verifyEmail('tok');
      await client.auth.requestPasswordReset('a@b.c');
      await client.auth.resetPassword('tok', 'pw');
      await client.auth.confirmEmailChange('tok');
      await client.auth.revertEmailChange('tok');
      await client.auth.acceptInvitation('inv', 'pw', locale: 'de');
      expect(
        t.requests.map((r) => r.url.path).toList(),
        [
          '/v1/shelf/_auth/verify-email/resend',
          '/v1/shelf/_auth/verify-email',
          '/v1/shelf/_auth/reset-password/request',
          '/v1/shelf/_auth/reset-password',
          '/v1/shelf/_auth/confirm-email-change',
          '/v1/shelf/_auth/revert-email-change',
          '/v1/shelf/_auth/accept-invitation',
        ],
      );
    });
  });
}

Map<String, Object?> meJson({List<String> reauthMethods = const ['password']}) => {
      'id': 'u1',
      'email': 'a@b.c',
      'email_verified': true,
      'roles': <String>[],
      'locale': 'en',
      'created_at': '2026-01-01T00:00:00Z',
      'identities': [
        {
          'provider': 'password',
          'email': 'a@b.c',
          'email_verified': true,
          'created_at': '2026-01-01T00:00:00Z',
          'last_used_at': null,
        },
      ],
      'reauth_methods': reauthMethods,
      'sign_in_methods': ['password'],
    };
