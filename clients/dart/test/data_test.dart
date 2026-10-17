import 'dart:convert';

import 'package:backd/backd.dart';
import 'package:test/test.dart';

import 'helpers.dart';

Map<String, Object?> pageJson(
  List<Object?> items, {
  int limit = 20,
  int skip = 0,
  bool hasMore = false,
  String? nextCursor,
  int? total,
}) =>
    {
      'items': items,
      'limit': limit,
      'skip': skip,
      'has_more': hasMore,
      if (nextCursor != null) 'next_cursor': nextCursor,
      if (total != null) 'total': total,
    };

Map<String, Object?> docJson(String id) => {
      'id': id,
      'title': 'post $id',
      '_meta': {'version': 3, 'created_at': 'x', 'updated_at': 'y'},
    };

void main() {
  group('list', () {
    test('builds the query: where JSON, orderBy join, paging, count', () async {
      final t = FakeTransport()..answer(respond(200, pageJson([])));
      final posts = testClient(t, apiKey: 'k').db('main').collection('posts');
      await posts.list(
        where: {
          'price': {'\$lt': 20},
        },
        orderBy: ['-_meta.created_at', 'title'],
        limit: 50,
        skip: 10,
        count: true,
        fileLinks: true,
        deleted: DeletedDocs.include,
        language: 'es',
      );
      final q = t.requests.single.url.queryParameters;
      expect(jsonDecode(q['where']!), {'price': {'\$lt': 20}});
      expect(q['order_by'], '-_meta.created_at,title');
      expect(q['limit'], '50');
      expect(q['skip'], '10');
      expect(q['count'], 'true');
      expect(q['file_links'], 'true');
      expect(q['deleted'], 'include');
      expect(q['language'], 'es');
      expect(t.requests.single.url.path, '/v1/shelf/main/posts');
    });

    test('where as a string is sent as-is', () async {
      final t = FakeTransport()..answer(respond(200, pageJson([])));
      await testClient(t, apiKey: 'k')
          .db('main')
          .collection('posts')
          .list(where: '{"published":true}');
      expect(t.requests.single.url.queryParameters['where'],
          '{"published":true}');
    });

    test('single orderBy string and expand', () async {
      final t = FakeTransport()..answer(respond(200, pageJson([])));
      await testClient(t, apiKey: 'k').db('main').collection('posts').list(
            orderBy: 'title',
            expand: expand([('author', ['name']), 'tags']),
          );
      final q = t.requests.single.url.queryParameters;
      expect(q['order_by'], 'title');
      expect(q['expand'], 'author(name),tags');
    });

    test('items come back as docs', () async {
      final t = FakeTransport()
        ..answer(respond(200, pageJson([docJson('a'), docJson('b')], total: 2)));
      final page = await testClient(t, apiKey: 'k')
          .db('main')
          .collection('posts')
          .list(count: true);
      expect(page.items, hasLength(2));
      expect(page.items[0]['id'], 'a');
      expect(page.items[0]['_meta'], isA<Map<String, Object?>>());
      expect(page.total, 2);
      expect(page.hasMore, isFalse);
    });
  });

  group('iterate', () {
    test('follows next_cursor through the pages', () async {
      final t = FakeTransport()
        ..answer(respond(
            200, pageJson([docJson('1')], hasMore: true, nextCursor: 'c1')))
        ..answer(respond(
            200, pageJson([docJson('2')], hasMore: true, nextCursor: 'c2')))
        ..answer(respond(200, pageJson([docJson('3')])));
      final docs = await testClient(t, apiKey: 'k')
          .db('main')
          .collection('posts')
          .iterate(limit: 1)
          .toList();
      expect(docs.map((d) => d['id']), ['1', '2', '3']);
      expect(t.requests[1].url.queryParameters['after'], 'c1');
      expect(t.requests[2].url.queryParameters['after'], 'c2');
      // The cursor disables the offset.
      expect(t.requests[1].url.queryParameters.containsKey('skip'), isFalse);
    });

    test('without a cursor falls back to skip', () async {
      final t = FakeTransport()
        ..answer(respond(200, pageJson([docJson('1')], hasMore: true)))
        ..answer(respond(200, pageJson([docJson('2')])));
      await testClient(t, apiKey: 'k')
          .db('main')
          .collection('posts')
          .iterate(limit: 1)
          .toList();
      expect(t.requests[1].url.queryParameters['skip'], '1');
    });
  });

  group('get and writes', () {
    test('get sends file_links/expand/deleted', () async {
      final t = FakeTransport()..answer(respond(200, docJson('p1')));
      final doc = await testClient(t, apiKey: 'k')
          .db('main')
          .collection('posts')
          .get('p1', fileLinks: true, expand: 'author');
      expect(doc['id'], 'p1');
      final q = t.requests.single.url.queryParameters;
      expect(q['file_links'], 'true');
      expect(q['expand'], 'author');
      expect(t.requests.single.url.path, '/v1/shelf/main/posts/p1');
    });

    test('create posts the doc; org/team scope; idempotency header', () async {
      final t = FakeTransport()..answer(respond(201, docJson('n1')));
      final posts = testClient(t, apiKey: 'k')
          .db('main')
          .collection('posts')
          .inOrg('acme');
      final doc =
          await posts.create({'title': 'hi'}, idempotencyKey: 'k-1', team: 'dev');
      expect(doc['id'], 'n1');
      final req = t.requests.single;
      expect(req.method, 'POST');
      expect(req.url.queryParameters['org'], 'acme');
      expect(req.url.queryParameters['team'], 'dev');
      expect(headerOf(req, 'Idempotency-Key'), 'k-1');
      expect(jsonDecode(utf8.decode(req.body!)), {'title': 'hi'});
    });

    test('replace puts the doc; ifMatch becomes If-Match', () async {
      final t = FakeTransport()..answer(respond(200, docJson('p1')));
      await testClient(t, apiKey: 'k')
          .db('main')
          .collection('posts')
          .replace('p1', {'title': 'new'}, ifMatch: 7);
      final req = t.requests.single;
      expect(req.method, 'PUT');
      expect(headerOf(req, 'If-Match'), '"7"');
    });

    test('patch uses merge-patch content type; ifMatch as given', () async {
      final t = FakeTransport()..answer(respond(200, docJson('p1')));
      await testClient(t, apiKey: 'k')
          .db('main')
          .collection('posts')
          .patch('p1', {'title': 'new'}, ifMatch: '"2", "3"');
      final req = t.requests.single;
      expect(req.method, 'PATCH');
      expect(headerOf(req, 'Content-Type'), 'application/merge-patch+json');
      expect(headerOf(req, 'If-Match'), '"2", "3"');
    });

    test('delete and purge', () async {
      final t = FakeTransport()
        ..answer(respond(204))
        ..answer(respond(204));
      final posts = testClient(t, apiKey: 'k').db('main').collection('posts');
      await posts.delete('p1');
      await posts.delete('p2', purge: true, ifMatch: 4);
      expect(t.requests[0].method, 'DELETE');
      expect(t.requests[0].url.path, '/v1/shelf/main/posts/p1');
      expect(t.requests[1].url.queryParameters['purge'], 'true');
      expect(headerOf(t.requests[1], 'If-Match'), '"4"');
    });

    test('restore posts to the restore route', () async {
      final t = FakeTransport()..answer(respond(200, docJson('p1')));
      await testClient(t, apiKey: 'k')
          .db('main')
          .collection('posts')
          .restore('p1');
      expect(t.requests.single.method, 'POST');
      expect(t.requests.single.url.path, '/v1/shelf/main/posts/p1/restore');
    });
  });

  group('batch', () {
    test('serializes the operations with if_match', () async {
      final t = FakeTransport()
        ..answer(respond(200, {
          'results': [docJson('a'), {'id': 'b'}],
        }));
      final results = await testClient(t, apiKey: 'k').db('main').batch(
        [
          const BatchCreate(collection: 'posts', document: {'title': 'x'}),
          const BatchReplace(
              collection: 'posts', id: 'p2', document: {'title': 'y'}, ifMatch: 3),
          const BatchPatch(
              collection: 'posts', id: 'p3', patch: {'title': 'z'}, ifMatch: '*'),
          const BatchDelete(collection: 'posts', id: 'p4', ifMatch: '"1"'),
        ],
        idempotencyKey: 'batch-1',
      );
      final body = jsonDecode(utf8.decode(t.requests.single.body!)) as Map;
      final ops = body['operations'] as List;
      expect(ops[0], {'op': 'create', 'collection': 'posts', 'document': {'title': 'x'}});
      expect(ops[1]['if_match'], '"3"');
      expect(ops[2]['if_match'], '*');
      expect(ops[3]['if_match'], '"1"');
      expect(t.requests.single.url.path, '/v1/shelf/main/_batch');
      expect(headerOf(t.requests.single, 'Idempotency-Key'), 'batch-1');
      expect(results, hasLength(2));
      expect(results[1]['id'], 'b');
    });
  });

  group('expand', () {
    test('names, nested names, fields', () {
      expect(expand(['author']), 'author');
      expect(expand(['author', 'tags']), 'author,tags');
      expect(expand(['post.author']), 'post.author');
      expect(expand([('author', ['name', 'avatar'])]), 'author(name,avatar)');
      expect(
        expand([('author', ['name']), 'tags']),
        'author(name),tags',
      );
    });

    test('rejects bad names and empty fields', () {
      expect(() => expand(['9bad']), throwsArgumentError);
      expect(() => expand(['a-b']), throwsArgumentError);
      expect(() => expand([('author', ['na me'])]), throwsArgumentError);
      expect(() => expand([42]), throwsArgumentError);
    });
  });
}
