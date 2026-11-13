import 'dart:convert';

import 'package:backd/backd.dart';
import 'package:test/test.dart';

import 'helpers.dart';

Map<String, Object?> sessionJson(String token) => {
      'token': token,
      'token_type': 'Bearer',
      'session_id': 's1',
      'expires_at': '2030-01-01T00:00:00Z',
      'user': {
        'id': 'u1',
        'email': 'a@b.c',
        'email_verified': true,
        'roles': <String>[],
        'locale': 'en',
        'created_at': '2026-01-01T00:00:00Z',
      },
    };

const options = {
  'publicKey': {'challenge': 'AAEC', 'rpId': 'localhost'},
};
const credential = {
  'id': 'AQID',
  'rawId': 'AQID',
  'type': 'public-key',
  'response': {'clientDataJSON': 'BAU'},
};
const passkey = {
  'id': 'p1',
  'name': 'Laptop',
  'created_at': 't',
  'last_used_at': null,
  'transports': ['internal'],
  'backup_eligible': true,
  'backup_state': false,
};

Map<String, Object?> bodyOf(TransportRequest r) =>
    jsonDecode(utf8.decode(r.body!)) as Map<String, Object?>;

void main() {
  test('list, rename and remove', () async {
    final t = FakeTransport()
      ..answer(respond(200, {'items': [passkey]}))
      ..answer(respond(200, {...passkey, 'name': 'Work'}))
      ..answer(respond(204));
    final client = testClient(t);
    client.proof = ReauthProof(
        token: 'r1', expires: DateTime.now().add(const Duration(minutes: 5)));
    final list = await client.auth.passkeys.list();
    expect(list.single.name, 'Laptop');
    expect(list.single.backupEligible, isTrue);
    expect((await client.auth.passkeys.rename('p1', 'Work')).name, 'Work');
    expect(bodyOf(t.requests[1]), {'name': 'Work'});
    await client.auth.passkeys.remove('p1');
    expect(t.requests[2].method, 'DELETE');
    expect(headerOf(t.requests[2], 'X-Backd-Reauth'), 'r1');
  });

  test('registerWithOptions hands the options to the callback and sends its credential', () async {
    final t = FakeTransport()
      ..answer(respond(200, {'challenge_id': 'c1', 'options': options}))
      ..answer(respond(201, passkey));
    final client = testClient(t);
    client.proof = ReauthProof(
        token: 'r1', expires: DateTime.now().add(const Duration(minutes: 5)));
    Map<String, Object?>? seen;
    final made = await client.auth.passkeys.registerWithOptions((o) async {
      seen = o;
      return credential;
    }, name: 'Laptop');
    expect(seen, options);
    expect(made.id, 'p1');
    expect(bodyOf(t.requests[1]),
        {'challenge_id': 'c1', 'credential': credential, 'name': 'Laptop'});
    expect(headerOf(t.requests[0], 'X-Backd-Reauth'), 'r1');
  });

  test('signInWithOptions is two calls, stores the session and sends no credentials', () async {
    final storage = MemoryTokenStorage();
    final t = FakeTransport()
      ..answer(respond(200,
          {'challenge_id': 'c1', 'strategy': 'passkey', 'options': options}))
      ..answer(respond(200, sessionJson('bds_pk')));
    final client = testClient(t, storage: storage);
    final result = await client.auth.passkeys
        .signInWithOptions((o) async => credential, strategy: 'passkey');
    expect((result as Session).token, 'bds_pk');
    expect(await storage.get(), 'bds_pk');
    expect(bodyOf(t.requests[0]), {'strategy': 'passkey'});
    expect(bodyOf(t.requests[1]),
        {'challenge_id': 'c1', 'credential': credential});
    expect(headerOf(t.requests[1], 'Authorization'), isNull);
    expect(t.requests[0].url.path, '/v1/shelf/_auth/passkey/login/options');
  });

  test('a cancelled prompt makes no verify call', () async {
    final t = FakeTransport()
      ..answer(respond(200, {'challenge_id': 'c1', 'options': options}));
    final client = testClient(t);
    await expectLater(
      client.auth.passkeys.signInWithOptions((o) async => throw StateError('cancelled')),
      throwsA(isA<StateError>()),
    );
    expect(t.requests, hasLength(1));
  });

  test('a passkey step, an enrolment and a reauthentication', () async {
    final t = FakeTransport()
      ..answer(respond(200, {'options': options}))
      ..answer(respond(200, sessionJson('bds_two')))
      ..answer(respond(200, {'challenge_id': 'c9', 'options': options}))
      ..answer(respond(200, {
        'mfa_enrollment_required': true,
        'challenge': 'ch2',
        'next': ['passkey'],
        'strategy': 's',
      }))
      ..answer(respond(200, {'options': options}))
      ..answer(respond(200,
          {'reauth_token': 'r1', 'expires_at': '2030-01-01T00:00:00Z'}));
    final client = testClient(t);
    final step = await client.auth.passkeys
        .stepWithOptions('ch', (o) async => credential);
    expect((step as Session).token, 'bds_two');
    expect(bodyOf(t.requests[1]),
        {'challenge': 'ch', 'method': 'passkey', 'credential': credential});

    final enrol = await client.auth.passkeys
        .enrollWithOptions('ch', (o) async => credential, name: 'Phone');
    expect(enrol, isA<EnrollmentChallenge>());
    expect((enrol as EnrollmentChallenge).next, ['passkey']);
    expect(bodyOf(t.requests[3]),
        {'challenge': 'ch', 'credential': credential, 'name': 'Phone'});

    final proof = await client.auth.passkeys
        .reauthWithOptions((o) async => credential);
    expect(proof.reauthToken, 'r1');
    expect(bodyOf(t.requests[5]), {'method': 'passkey', 'credential': credential});
  });

  test('a code step is unchanged', () async {
    final t = FakeTransport()..answer(respond(200, sessionJson('bds_3')));
    await testClient(t).auth.verifyStep('ch', 'totp', '123456');
    expect(bodyOf(t.requests.single),
        {'challenge': 'ch', 'method': 'totp', 'code': '123456'});
  });
}
