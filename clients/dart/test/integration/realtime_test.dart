// Realtime against a real backd: a live view sees the create a moment after
// it happens. Needs `make dart-integration`.
@TestOn('vm')
library;

import 'dart:async';

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

      await view.ready.timeout(const Duration(seconds: 15));

      final changed = Completer<void>();
      view.on('change', (_) {
        if (!changed.isCompleted) changed.complete();
      });
      view.on('error', (Object? e) {
        if (!changed.isCompleted) {
          changed.completeError(
              e is Object ? e : StateError('realtime error'));
        }
      });

      final created = await c.db('app').collection('posts').create(
          {'title': 'live-post', 'published': true});

      await changed.future.timeout(const Duration(seconds: 15));

      final ids = view.items.map((d) => d['id']);
      expect(ids, contains(created['id']));

      view.close();
    });
  });
}
