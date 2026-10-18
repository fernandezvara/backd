// Documents against a real backd + MongoDB: create, read, patch, the rules,
// the query language, batch. Needs `make dart-integration`.
@TestOn('vm')
library;

import 'package:backd/backd.dart';
import 'package:test/test.dart';

import 'env.dart' as env;

void main() {
  group('collections', skip: env.skip, () {
    test('crud, rules and where', () async {
      final u = await env.user('crud');
      final posts = u.client.db('app').collection('posts');
      addTearDown(() async => u.client.close());

      final post = await posts
          .create({'title': 'hello', 'slug': 'hello', 'published': true});
      final id = post['id'] as String;

      expect((await posts.get(id))['title'], 'hello');

      // The update rule forbids touching `status`.
      expect(
        () => posts.patch(id, {'status': 'review'}),
        throwsA(isA<BackdException>().having((e) => e.status, 'status', 403)),
      );

      final mine = await posts
          .list(where: {'title': 'hello', 'published': true}, limit: 10);
      expect(mine.items.map((d) => d['id']), contains(id));

      // Another user cannot read an unpublished post they do not own.
      final other = await env.user('other');
      addTearDown(() async => other.client.close());
      await posts.patch(id, {'published': false});
      expect(
        () => other.client.db('app').collection('posts').get(id),
        throwsA(isA<BackdException>()),
      );
    });

    test('batch applies create and patch atomically', () async {
      final u = await env.user('batch');
      final posts = u.client.db('app').collection('posts');
      addTearDown(() async => u.client.close());

      final stock = await posts.create({'title': 'stock', 'published': true});
      final results = await u.client.db('app').batch([
        BatchCreate(
            collection: 'posts',
            document: {'title': 'second', 'published': true}),
        BatchPatch(
            collection: 'posts',
            id: stock['id'] as String,
            patch: {'title': 'stock-updated'}),
      ]);
      expect(results, hasLength(2));
      expect(results[0]['title'], 'second');
      expect(
          (await posts.get(stock['id'] as String))['title'], 'stock-updated');
    });
  });
}
