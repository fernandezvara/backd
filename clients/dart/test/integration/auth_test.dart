// Auth against a real backd: sign up, read the account, sign out, sign back
// in. Needs `make dart-integration` (or BACKD_URL set to a compatible stack).
@TestOn('vm')
library;

import 'package:backd/backd.dart';
import 'package:test/test.dart';

import 'env.dart' as env;

void main() {
  group('account', skip: env.skip, () {
    test('signup, me, logout, login', () async {
      final u = await env.user('ada');
      final c = u.client;
      addTearDown(() async => c.close());

      final me = await c.auth.me();
      expect(me.email, u.email);

      // A second client logs in with the same account.
      final again = BackdClient<Object?>(url: env.url, realm: 'itest');
      addTearDown(() async => again.close());
      await again.auth.login(u.email, env.password);
      expect((await again.auth.me()).email, u.email);

      await c.auth.logout();
      expect(c.auth.me(), throwsA(isA<Exception>()));
    });
  });
}
