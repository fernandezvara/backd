import 'package:backd/backd.dart';
import 'package:test/test.dart';

import 'helpers.dart';

TransportResponse post(String who) => respond(
      200,
      {'id': 'p1', 'title': 'seen by $who'},
      {'ETag': '"1"'},
    );

void main() {
  test('asUser sends X-Backd-On-Behalf-Of and keeps the key', () async {
    final t = FakeTransport()
      ..answer(post('u_ada'))
      ..answer(post('key'));
    final key = testClient(t, apiKey: 'bdk_secret');
    final ada = key.asUser('u_ada');
    final got = await ada.db('main').collection('posts').get('p1');
    expect(got['title'], 'seen by u_ada');
    await key.db('main').collection('posts').get('p1');
    expect(t.requests[0].headers['X-Backd-On-Behalf-Of'], 'u_ada');
    expect(t.requests[0].headers['Authorization'], 'Bearer bdk_secret');
    expect(t.requests[1].headers.containsKey('X-Backd-On-Behalf-Of'), isFalse);
    expect(ada.onBehalfOf, 'u_ada');
    expect(key.onBehalfOf, isNull);
  });

  test('each acting client has a cache of its own', () async {
    final t = FakeTransport()
      ..answer(post('u_ada'))
      ..answer(post('u_bob'));
    final key = testClient(t, apiKey: 'bdk_secret');
    final ada = key.asUser('u_ada');
    final bob = key.asUser('u_bob');
    await ada.db('main').collection('posts').get('p1');
    final got = await bob.db('main').collection('posts').get('p1');
    expect(t.requests[1].headers.containsKey('If-None-Match'), isFalse,
        reason: 'bob must not revalidate with ada\'s tag');
    expect(got['title'], 'seen by u_bob');
    expect(ada.cacheStats().entries, 1);
    expect(bob.cacheStats().entries, 1);
    expect(key.cacheStats().entries, 0);
  });

  test('asUser needs a key and an id, and cannot be chained', () {
    final t = FakeTransport();
    expect(() => testClient(t).asUser('u1'), throwsStateError);
    final key = testClient(t, apiKey: 'bdk_secret');
    expect(() => key.asUser('  '), throwsArgumentError);
    expect(() => key.asUser('u_ada').asUser('u_bob'), throwsStateError);
  });

  test('an acting client refuses the account routes before sending', () async {
    final t = FakeTransport();
    final ada = testClient(t, apiKey: 'bdk_secret').asUser('u_ada');
    await expectLater(ada.auth.me(), throwsStateError);
    expect(t.requests, isEmpty);
  });
}
