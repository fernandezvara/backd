import 'dart:convert';
import 'dart:io';

import 'package:backd/backd.dart';
import 'package:test/test.dart';

import 'helpers.dart';

Map<String, Object?> scoped(String token, String org) => {
      'token': token,
      'token_type': 'Bearer',
      'session_id': 's1',
      'expires_at': '2030-01-01T00:00:00Z',
      'org': org,
      'user': {
        'id': 'u1',
        'email': 'a@b.c',
        'email_verified': true,
        'roles': <String>[],
        'locale': 'en',
        'created_at': '2026-01-01T00:00:00Z',
      },
    };

Map<String, Object?> bodyOf(FakeTransport t, int i) =>
    jsonDecode(utf8.decode(t.requests[i].body!)) as Map<String, Object?>;

void main() {
  test('a client made with an org signs in to that organization', () async {
    final t = FakeTransport()..answer(respond(200, scoped('bds_1', 'o1')));
    final client = testClient(t, org: ' Acme-Corp ');
    final s = await client.auth.login('a@b.c', 'pw');
    expect(t.requests[0].url.path, '/v1/shelf/_auth/orgs/acme-corp/login');
    expect(bodyOf(t, 0), {'email': 'a@b.c', 'password': 'pw'});
    expect(s.org, 'o1');
    expect(client.auth.current?.org, 'o1');
  });

  test('login can name the organization and use an invitation', () async {
    final t = FakeTransport()..answer(respond(200, scoped('bds_1', 'o1')));
    await testClient(t)
        .auth
        .login('b@b.c', 'pw', org: 'o1', invitation: 'bdo_x');
    expect(t.requests[0].url.path, '/v1/shelf/_auth/orgs/o1/login');
    expect(bodyOf(t, 0),
        {'email': 'b@b.c', 'password': 'pw', 'invitation': 'bdo_x'});
  });

  test('without an organization the realm routes are used', () async {
    final t = FakeTransport()..answer(respond(200, scoped('bds_1', 'o1')));
    await testClient(t).auth.login('a@b.c', 'pw');
    expect(t.requests[0].url.path, '/v1/shelf/_auth/login');
  });

  test('signup, magic links and password resets go to the organization too',
      () async {
    final t = FakeTransport()
      ..answer(respond(201, scoped('bds_1', 'o1')))
      ..answer(respond(202, {'request_id': 'r1'}))
      ..answer(respond(202, {'status': 'accepted'}));
    final client = testClient(t, org: 'acme-corp');
    await client.auth.signup('n@b.c', 'dev-p4ssw0rd!', invitation: 'bdo_x');
    await client.auth.magicLink.request('a@b.c', redirectTo: 'myapp://in');
    await client.auth.requestPasswordReset('a@b.c');
    expect([
      for (final r in t.requests) r.url.path
    ], [
      '/v1/shelf/_auth/orgs/acme-corp/signup',
      '/v1/shelf/_auth/orgs/acme-corp/magic-link',
      '/v1/shelf/_auth/orgs/acme-corp/reset-password/request',
    ]);
    for (var i = 0; i < 3; i++) {
      expect(bodyOf(t, i).containsKey('org'), isFalse);
    }
  });

  test('options say what an organization offers', () async {
    final t = FakeTransport()
      ..answer(respond(200, {
        'items': ['google'],
        'methods': ['password', 'magic_link'],
      }))
      ..answer(respond(200, {
        'items': ['google']
      }));
    final client = testClient(t, org: 'acme-corp');
    final o = await client.auth.options();
    expect(o.providers, ['google']);
    expect(o.methods, ['password', 'magic_link']);
    expect(o.ssoRequired, false);
    expect(t.requests[0].url.path, '/v1/shelf/_auth/orgs/acme-corp/providers');
    expect(await client.auth.providers(org: 'other'), ['google']);
    expect(t.requests[1].url.path, '/v1/shelf/_auth/orgs/other/providers');
  });

  test('an organization that requires its connection says so', () async {
    final t = FakeTransport()
      ..answer(respond(200, {
        'items': ['google'],
        'methods': <String>[],
        'sso_required': true,
      }));
    final o = await testClient(t, org: 'acme-corp').auth.options();
    expect(o.ssoRequired, true);
    expect(o.methods, isEmpty);
    expect(o.providers, ['google']);
  });

  test('client.org gives one client per organization, each with its own token',
      () async {
    final t = FakeTransport()
      ..answer(respond(200, scoped('bds_a', 'oa')))
      ..answer(respond(200, scoped('bds_b', 'ob')))
      ..answer(respond(200, {'id': 'u1', 'email': 'a@b.c'}))
      ..answer(respond(200, {'id': 'u1', 'email': 'a@b.c'}));
    final root = testClient(t);
    final a = root.org('Acme');
    final b = root.org('beta');
    expect(identical(root.org('acme'), a), isTrue);
    expect(identical(a.org('acme'), a), isTrue);
    expect(identical(a.org('beta'), b), isTrue);
    await a.auth.login('a@b.c', 'pw');
    await b.auth.login('a@b.c', 'pw');
    expect(await a.storage.get(), 'bds_a');
    expect(await b.storage.get(), 'bds_b');
    expect(await root.storage.get(), isNull);
    await a.auth.me();
    await b.auth.me();
    expect(headerOf(t.requests[2], 'Authorization'), 'Bearer bds_a');
    expect(headerOf(t.requests[3], 'Authorization'), 'Bearer bds_b');
  });

  test('signing out of one organization leaves the others', () async {
    final t = FakeTransport()
      ..answer(respond(200, scoped('bds_a', 'oa')))
      ..answer(respond(200, scoped('bds_b', 'ob')))
      ..answer(respond(204));
    final root = testClient(t);
    final a = root.org('acme');
    final b = root.org('beta');
    await a.auth.login('a@b.c', 'pw');
    await b.auth.login('a@b.c', 'pw');
    await a.auth.logout();
    expect(await a.storage.get(), isNull);
    expect(await b.storage.get(), 'bds_b');
    expect([for (final s in await root.orgs.signedIn()) s.ref], ['beta']);
  });

  test('signedIn lists the organizations the app holds a session for',
      () async {
    final t = FakeTransport()
      ..answer(respond(200, scoped('bds_a', 'oa')))
      ..answer(respond(200, scoped('bds_b', 'ob')))
      ..answer(respond(200, {
        'id': 'u1',
        'email': 'a@b.c',
        'org': {'id': 'oa', 'name': 'Acme Corp', 'slug': 'acme'},
      }));
    final root = testClient(t);
    await root.org('acme').auth.login('a@b.c', 'pw');
    await root.org('beta').auth.login('a@b.c', 'pw');
    expect((await root.org('acme').auth.org())?['name'], 'Acme Corp');
    final list = await root.orgs.signedIn()
      ..sort((x, y) => x.ref.compareTo(y.ref));
    expect([for (final s in list) (s.ref, s.id, s.name)],
        [('acme', 'oa', 'Acme Corp'), ('beta', 'ob', null)]);
    expect(list.first.expiresAt, '2030-01-01T00:00:00Z');
  });

  test('a storage that cannot keep several tokens refuses client.org', () {
    final root = testClient(FakeTransport(), storage: _SingleToken());
    expect(() => root.org('acme'), throwsStateError);
    expect(() => testClient(FakeTransport()).org(' '), throwsArgumentError);
  });

  test('the file storage keeps a token per organization beside its own',
      () async {
    final dir = await Directory.systemTemp.createTemp('backd-org-');
    addTearDown(() => dir.delete(recursive: true));
    final path = '${dir.path}/session';
    final t = FakeTransport()..answer(respond(200, scoped('bds_a', 'oa')));
    final root = testClient(t, storage: FileTokenStorage(path));
    await root.org('acme').auth.login('a@b.c', 'pw');
    expect(await File('$path.acme').readAsString(), 'bds_a');
    // A later run finds it again.
    final again = testClient(FakeTransport(), storage: FileTokenStorage(path));
    expect(await again.org('acme').storage.get(), 'bds_a');
    expect([for (final s in await again.orgs.signedIn()) (s.ref, s.id)],
        [('acme', 'oa')]);
  });

  test('creating an organization signs the app in to it', () async {
    final t = FakeTransport()
      ..answer(respond(201, {
        'id': 'on',
        'name': 'New',
        'slug': 'new',
        'session': {
          'token': 'bds_n',
          'expires_at': '2030-01-01T00:00:00Z',
          'org': 'on'
        },
      }))
      ..answer(respond(200, {'id': 'u1', 'email': 'a@b.c'}));
    final root = testClient(t);
    final org = await root.orgs.create({'name': 'New', 'slug': 'new'});
    expect(org['id'], 'on');
    final c = root.org('new');
    expect(await c.storage.get(), 'bds_n');
    await c.auth.me();
    expect(headerOf(t.requests[1], 'Authorization'), 'Bearer bds_n');
  });

  test('a new person starts a company', () async {
    final t = FakeTransport()
      ..answer(respond(201, {
        ...scoped('bds_n', 'on'),
        'organization': {'id': 'on', 'name': 'Start Inc', 'slug': 'start-inc'},
      }));
    final root = testClient(t);
    final out = await root.auth.signUpWithOrg('f@b.c', 'dev-p4ssw0rd!',
        name: 'Start Inc', slug: 'start-inc');
    expect(t.requests[0].url.path, '/v1/shelf/_auth/orgs');
    expect(bodyOf(t, 0), {
      'email': 'f@b.c',
      'password': 'dev-p4ssw0rd!',
      'name': 'Start Inc',
      'slug': 'start-inc',
    });
    expect(out.organization['slug'], 'start-inc');
    expect(await root.org('start-inc').storage.get(), 'bds_n');
    expect(await root.storage.get(), isNull);
  });

  test('a company in an invite-only realm starts with the realm invitation',
      () async {
    final t = FakeTransport()
      ..answer(respond(201, {
        ...scoped('bds_n', 'on'),
        'organization': {'id': 'on', 'name': 'S', 'slug': 's-inc'},
      }));
    await testClient(t).auth.signUpWithOrg('f@b.c', 'dev-p4ssw0rd!',
        name: 'S', slug: 's-inc', invitation: 'bdi_x');
    expect(bodyOf(t, 0)['invitation'], 'bdi_x');
  });

  test('a company that needs its address verified first starts no session',
      () async {
    final t = FakeTransport()
      ..answer(respond(202, {'status': 'verification_required'}));
    final root = testClient(t);
    expect(
      root.auth
          .signUpWithOrg('f@b.c', 'dev-p4ssw0rd!', name: 'S', slug: 's-inc'),
      throwsA(isA<VerificationRequiredError>()),
    );
  });

  test('findOrgs asks for the email; previewInvitation needs no session',
      () async {
    final t = FakeTransport()
      ..answer(respond(202, {'status': 'accepted'}))
      ..answer(respond(200, {
        'org': {'id': 'o1', 'name': 'Acme'},
        'roles': ['editor'],
        'teams': <Object>[],
        'binding': 'email',
        'expires_at': 'x',
      }));
    final client = testClient(t, org: 'acme-corp');
    await client.auth.findOrgs('a@b.c');
    expect(t.requests[0].url.path, '/v1/shelf/_auth/orgs/find');
    expect(headerOf(t.requests[0], 'Authorization'), isNull);
    final pv = await client.auth.previewInvitation('bdo_x');
    expect(t.requests[1].url.path,
        '/v1/shelf/_auth/orgs/acme-corp/invitations/preview');
    expect((pv['org'] as Map)['name'], 'Acme');
    expect(testClient(t).auth.previewInvitation('bdo_x'), throwsArgumentError);
  });

  test(
      "a sign-in that returns for an organization is kept by that organization's client",
      () async {
    final t = FakeTransport()
      ..answer(respond(200, {'authorize_url': 'https://idp.test/authorize'}))
      ..answer(respond(200, scoped('bds_a', 'oa')));
    final root = testClient(t);
    await root.auth.signInWith('google', redirectTo: 'myapp://in');
    final out = await root.auth
        .completeSignIn(Uri.parse('myapp://in?backd_code=c1&backd_org=oa'));
    expect((out as Session).org, 'oa');
    expect(await root.org('oa').storage.get(), 'bds_a');
    expect(await root.storage.get(), isNull);
  });
}

class _SingleToken implements TokenStorage {
  String? _t;
  @override
  String? get() => _t;
  @override
  void set(String token) => _t = token;
  @override
  void remove() => _t = null;
  @override
  TokenStorage? slot(String name) => null;
  @override
  List<String> slots() => const [];
}
