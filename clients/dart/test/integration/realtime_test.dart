// Realtime against a real backd: a live view sees the create a moment after
// it happens. Needs `make dart-integration`.
@TestOn('vm')
library;

import 'package:backd/backd.dart';
import 'package:test/test.dart';

import 'env.dart' as env;

void main() {
  group('realtime', skip: env.skip, () {
    test('a live view delivers a new document', () async {
      final u = await env.user('live');
      final c = u.client;
      addTearDown(() async => c.close());

      final view = c.realtime.subscribe([
        SubscriptionSpec(
          db: 'app',
          collection: 'posts',
          where: {'published': true},
        ),
      ]);

      Object? viewError;
      view.on('error', (Object? e) => viewError = e);

      await view.ready.timeout(const Duration(seconds: 15));

      final created = await c.db('app').collection('posts').create(
          {'title': 'live-post', 'published': true});

      // A 'change' event can arrive before the state holds the document (or
      // fire for the initial snapshot): poll the items until the new doc is
      // there, with a deadline.
      final deadline = DateTime.now().add(const Duration(seconds: 15));
      while (!view.items.any((d) => d['id'] == created['id'])) {
        if (viewError != null) fail('live view error: $viewError');
        if (DateTime.now().isAfter(deadline)) {
          fail('the new document never arrived in the live view');
        }
        await Future<void>.delayed(const Duration(milliseconds: 100));
      }

      view.close();
    });
  });
}
