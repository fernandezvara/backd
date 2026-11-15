import 'package:backd/backd.dart';
import 'package:test/test.dart';

import 'helpers.dart';

TransportResponse doc() => respond(200, {'id': 'p1', 'title': 'T'});
TransportResponse page() => respond(200, {
      'items': [
        {'id': 'p1', 'title': 'T'}
      ],
      'limit': 20,
      'skip': 0,
      'has_more': false,
    });

void main() {
  test('fields go on reads as one comma-separated parameter', () async {
    final t = FakeTransport()
      ..answer(doc())
      ..answer(page());
    final posts = testClient(t).db('main').collection('posts');
    final one = await posts.get('p1', fields: ['title', 'address.city']);
    final p = await posts.list(fields: ['title'], limit: 5);
    expect(t.requests[0].url.queryParameters['fields'], 'title,address.city');
    expect(t.requests[1].url.queryParameters['fields'], 'title');
    expect(t.requests[1].url.queryParameters['limit'], '5');
    expect(one['title'], 'T');
    expect(p.items.first['id'], 'p1');
  });

  test('fields go on the answers of writes, next to the other options',
      () async {
    final t = FakeTransport()
      ..answer(respond(201, {'id': 'p1'}))
      ..answer(doc())
      ..answer(doc())
      ..answer(doc());
    final posts = testClient(t).db('main').collection('posts');
    await posts.create({'title': 'T'},
        fields: ['title'], idempotencyKey: 'k1', team: 'blue');
    await posts.replace('p1', {'title': 'T'}, fields: ['title'], ifMatch: 1);
    await posts.patch('p1', {'title': 'U'}, fields: ['_meta.version']);
    await posts.restore('p1', fields: ['title']);
    final q = [for (final r in t.requests) r.url.queryParameters];
    expect(q[0], {'fields': 'title', 'team': 'blue'});
    expect(t.requests[0].headers['Idempotency-Key'], 'k1');
    expect(q[1]['fields'], 'title');
    expect(t.requests[1].headers['If-Match'], '"1"');
    expect(q[2]['fields'], '_meta.version');
    expect(q[3]['fields'], 'title');
  });

  test('without fields nothing is added', () async {
    final t = FakeTransport()..answer(doc());
    await testClient(t).db('main').collection('posts').get('p1');
    expect(t.requests[0].url.queryParameters.containsKey('fields'), isFalse);
  });

  test('fields must be a non-empty list of paths', () async {
    final t = FakeTransport();
    final posts = testClient(t).db('main').collection('posts');
    await expectLater(posts.get('p1', fields: []), throwsArgumentError);
    await expectLater(posts.get('p1', fields: ['title', ' ']), throwsArgumentError);
    await expectLater(posts.list(fields: []), throwsArgumentError);
    expect(t.requests, isEmpty);
  });
}
