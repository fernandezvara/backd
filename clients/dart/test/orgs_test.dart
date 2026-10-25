import 'dart:convert';

import 'package:backd/backd.dart';
import 'package:test/test.dart';

import 'helpers.dart';

void main() {
  group('orgs', () {
    test('create and get hit the _orgs routes', () async {
      final t = FakeTransport()
        ..answer(respond(200, {'id': 'org_1', 'name': 'Acme'}))
        ..answer(respond(200, {'id': 'org_1', 'name': 'Acme'}));
      final orgs = testClient(t, apiKey: 'k').orgs;
      expect((await orgs.create({'name': 'Acme'}))['id'], 'org_1');
      expect(t.requests[0].method, 'POST');
      expect(t.requests[0].url.path, '/v1/shelf/_orgs');
      await orgs.get('org_1');
      expect(t.requests[1].method, 'GET');
      expect(t.requests[1].url.path, '/v1/shelf/_orgs/org_1');
    });

    test('update requires reauth only for require_mfa', () async {
      final t = FakeTransport()
        ..answer(respond(200, {'id': 'org_1'}))
        ..answer(respond(200, {'id': 'org_1'}));
      final orgs = testClient(t, apiKey: 'k').orgs;
      await orgs.update('org_1', name: 'New');
      await orgs.update('org_1', requireMfa: true);
      expect(jsonDecode(utf8.decode(t.requests[1].body!)),
          {'require_mfa': true});
    });

    test('leave and delete', () async {
      final t = FakeTransport()
        ..answer(respond(204))
        ..answer(respond(204));
      final orgs = testClient(t, apiKey: 'k').orgs;
      await orgs.leave('org_1');
      expect(t.requests[0].url.path, '/v1/shelf/_orgs/org_1/leave');
      await orgs.delete('org_1');
      expect(t.requests[1].method, 'DELETE');
      expect(t.requests[1].url.path, '/v1/shelf/_orgs/org_1');
    });
  });

  group('members', () {
    test('list, update, remove', () async {
      final t = FakeTransport()
        ..answer(respond(200, {
          'items': [
            {'user': 'u_1', 'roles': ['editor']},
          ],
          'has_more': false,
        }))
        ..answer(respond(200, {'user': 'u_1'}))
        ..answer(respond(204));
      final members = testClient(t, apiKey: 'k').orgs.members('org_1');
      final page = await members.list(limit: 10, after: 'u_0');
      expect(t.requests[0].url.queryParameters['after'], 'u_0');
      expect((page['items'] as List).single['user'], 'u_1');
      await members.update('u_1', ['admin']);
      expect(jsonDecode(utf8.decode(t.requests[1].body!)),
          {'roles': ['admin']});
      await members.remove('u_1');
      expect(t.requests[2].url.path, '/v1/shelf/_orgs/org_1/members/u_1');
    });
  });

  group('teams', () {
    test('team CRUD and member/role routes', () async {
      final t = FakeTransport()
        ..answer(respond(200, {
          'items': [
            {'id': 't_1', 'name': 'Editors'},
          ],
        }))
        ..answer(respond(200, {'id': 't_2'}))
        ..answer(respond(200, {'items': <Object?>[], 'has_more': false}))
        ..answer(respond(200, {'user': 'u_1'}))
        ..answer(respond(200, {'user': 'u_1'}))
        ..answer(respond(200, {'user': 'u_1'}))
        ..answer(respond(200, {'user': 'u_1'}))
        ..answer(respond(204));
      final teams = testClient(t, apiKey: 'k').orgs.teams('org_1');
      await teams.list();
      await teams.create({'name': 'Editors', 'roles': ['editor']});
      await teams.members('t_1', role: 'editor');
      expect(t.requests[2].url.path,
          '/v1/shelf/_orgs/org_1/teams/t_1/members');
      expect(t.requests[2].url.queryParameters['role'], 'editor');
      await teams.grantRole('t_1', {'user': 'u_1', 'role': 'lead'});
      expect(t.requests[3].url.path,
          '/v1/shelf/_orgs/org_1/teams/t_1/roles');
      await teams.setRoleWindow('t_1', {'user': 'u_1', 'role': 'lead'});
      expect(t.requests[4].method, 'PATCH');
      await teams.revokeRole('t_1', {'user': 'u_1', 'role': 'lead'});
      expect(t.requests[5].method, 'DELETE');
      await teams.addMember('t_1', 'u_1');
      expect(t.requests[6].method, 'PUT');
      await teams.removeMember('t_1', 'u_1');
      expect(t.requests[7].url.path,
          '/v1/shelf/_orgs/org_1/teams/t_1/members/u_1');
    });
  });

  group('templates', () {
    test('list, get, put, delete, preview, test', () async {
      final t = FakeTransport()
        ..answer(respond(200, {'customizable': <String, Object?>{}, 'items': <Object?>[]}))
        ..answer(respond(200, {'kind': 'invite', 'locale': 'en'}))
        ..answer(respond(200, {'kind': 'invite', 'version': 2}))
        ..answer(respond(204))
        ..answer(respond(200, {'subject': 'Hi', 'html': '<p>x</p>'}))
        ..answer(respond(204));
      final tpl = testClient(t, apiKey: 'k').orgs.templates('org_1');
      await tpl.list();
      await tpl.get('invite', 'en');
      await tpl.put('invite', 'en',
          {'subject': 's', 'text': 't', 'html': 'h'});
      expect(t.requests[2].method, 'PUT');
      await tpl.delete('invite', 'en');
      await tpl.preview('invite', 'en', data: 'real');
      expect(jsonDecode(utf8.decode(t.requests[4].body!)), {'data': 'real'});
      await tpl.test('invite', 'en', draft: {'subject': 'd'});
      expect(jsonDecode(utf8.decode(t.requests[5].body!)), {'subject': 'd'});
    });
  });

  group('invitations', () {
    test('create serializes expires_in/max_uses; list, revoke, link, send',
        () async {
      final t = FakeTransport()
        ..answer(
            respond(200, {'id': 'inv_1', 'token': 'tok', 'link': null}))
        ..answer(respond(200, {'items': <Object?>[]}))
        ..answer(respond(204))
        ..answer(respond(200, {'id': 'inv_1', 'token': 'tok2'}))
        ..answer(respond(204));
      final inv = testClient(t, apiKey: 'k').orgs.invitations('org_1');
      final created = await inv.create(
          binding: 'email',
          email: 'a@b.c',
          roles: ['editor'],
          send: true,
          expiresIn: '2d');
      expect(created['token'], 'tok');
      final body = jsonDecode(utf8.decode(t.requests[0].body!));
      expect(body['expires_in'], '2d');
      expect(body['binding'], 'email');
      await inv.list();
      await inv.revoke('inv_1');
      await inv.newLink('inv_1');
      expect(t.requests[3].url.path,
          '/v1/shelf/_orgs/org_1/invitations/inv_1/link');
      await inv.send('inv_1');
      expect(t.requests[4].url.path,
          '/v1/shelf/_orgs/org_1/invitations/inv_1/send');
    });
  });

  group('push', () {
    test('register, devices, unregister (own id remembered)', () async {
      final storage = MemoryTokenStorage();
      await storage.set('tok');
      final t = FakeTransport()
        ..answer(respond(200, {'id': 'dev_1', 'platform': 'android'}))
        ..answer(respond(200, {
          'items': [
            {'id': 'dev_1'},
          ],
        }))
        ..answer(respond(204));
      final push = testClient(t, storage: storage).push;
      final device = await push.register(const DeviceInput(
          platform: 'android', token: 'fcm-token', name: 'Pixel'));
      expect(t.requests[0].url.path, '/v1/shelf/_auth/devices');
      expect(jsonDecode(utf8.decode(t.requests[0].body!)), {
        'platform': 'android',
        'token': 'fcm-token',
        'name': 'Pixel',
      });
      expect(device['id'], 'dev_1');
      expect(push.deviceId, 'dev_1');
      await push.devices();
      expect(await push.unregister(), isTrue);
      expect(t.requests[2].url.path, '/v1/shelf/_auth/devices/dev_1');
      expect(push.deviceId, isNull);
      expect(await push.unregister(), isFalse); // nothing registered
    });

    test('signing out forgets the device', () async {
      final storage = MemoryTokenStorage();
      await storage.set('tok');
      final t = FakeTransport()
        ..answer(respond(200, {'id': 'dev_1'}))
        ..answer(respond(204));
      final client = testClient(t, storage: storage);
      await client.push
          .register(const DeviceInput(platform: 'ios', token: 'apns'));
      expect(client.push.deviceId, 'dev_1');
      await client.auth.logout();
      // The auth event is delivered on the next microtask.
      await Future<void>.delayed(Duration.zero);
      expect(client.push.deviceId, isNull);
    });
  });

  group('consents', () {
    test('current and text are public; state/accept/withdraw use _auth',
        () async {
      final t = FakeTransport()
        ..answer(respond(200, {
          'items': [
            {'name': 'terms', 'required': true},
          ],
        }))
        ..answer(respond(
            200, {'name': 'terms', 'version': '1', 'text': 'md'}))
        ..answer(respond(200, {'consents': <String, Object?>{}, 'to_accept': <Object?>[]}))
        ..answer(respond(200, {'consents': <String, Object?>{'terms': '1'}}))
        ..answer(respond(200, {'consents': <String, Object?>{}}));
      final consents = testClient(t, apiKey: 'k').consents;
      final cur = await consents.current();
      expect(t.requests[0].url.path, '/v1/shelf/_consents');
      expect(cur.single['name'], 'terms');
      await consents.text('terms', '1', locale: 'es');
      expect(t.requests[1].url.path, '/v1/shelf/_consents/terms/1');
      expect(t.requests[1].url.queryParameters['locale'], 'es');
      await consents.state();
      expect(t.requests[2].url.path, '/v1/shelf/_auth/consents');
      await consents.accept({'terms': '1'});
      expect(t.requests[3].method, 'POST');
      await consents.withdraw('terms');
      expect(t.requests[4].url.path, '/v1/shelf/_auth/consents/terms');
    });
  });
}
