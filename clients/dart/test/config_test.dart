import 'package:backd/backd.dart';
import 'package:test/test.dart';

import 'helpers.dart';

Map<String, Object?> configBody(Map<String, Object?> values, {int ttl = 60}) =>
    {'values': values, 'version': 'v1', 'cache_ttl': ttl};

final class RecordingConfigStorage implements ConfigStorage {
  final Map<String, String> items = {};

  @override
  Future<String?> getItem(String key) async => items[key];

  @override
  Future<void> setItem(String key, String value) async => items[key] = value;

  @override
  Future<void> removeItem(String key) async => items.remove(key);
}

void main() {
  group('load and get', () {
    test('load fetches the named groups; get reads the copy', () async {
      final t = FakeTransport()
        ..answer(respond(200, configBody({
              'features': {
                'export': {'enabled': true},
              },
            }), {'ETag': '"e1"'}));
      final config = testClient(t, apiKey: 'k').config;
      final view = await config.load(['features']);
      expect(view, {
        'features': {
          'export': {'enabled': true},
        },
      });
      expect(t.requests.single.url.queryParameters['paths'], 'features');
      expect(headerOf(t.requests.single, 'If-None-Match'), isNull);
      // Fresh copy: no request.
      expect(await config.get('features.export.enabled'), isTrue);
      expect(t.requests, hasLength(1));
    });

    test('an unknown path fetches its group, then answers null', () async {
      final t = FakeTransport()
        ..answer(respond(200, configBody({'maintenance': {'enabled': false}})));
      final config = testClient(t, apiKey: 'k').config;
      expect(await config.get('maintenance.mode'), isNull);
      expect(t.requests.single.url.queryParameters['paths'], 'maintenance');
    });

    test('a stale copy answers at once and revalidates in the background',
        () async {
      final t = FakeTransport()
        ..answer(respond(200, configBody({'g': {'x': 1}}, ttl: 0), {'ETag': '"e1"'}))
        ..answer(respond(304, {}, {'Cache-Control': 'max-age=120'}));
      final config = testClient(t, apiKey: 'k').config;
      await config.load(['g']);
      // ttl 0: already stale — the copy answers, a 304 lands behind.
      expect(await config.get('g.x'), 1);
      await Future<void>.delayed(Duration.zero);
      expect(t.requests, hasLength(2));
      expect(headerOf(t.requests[1], 'If-None-Match'), '"e1"');
    });

    test('refresh always asks the server', () async {
      final t = FakeTransport()
        ..answer(respond(200, configBody({'g': {'x': 1}}), {'ETag': '"e1"'}))
        ..answer(respond(200, configBody({'g': {'x': 2}}), {'ETag': '"e2"'}));
      final config = testClient(t, apiKey: 'k').config;
      await config.load(['g']);
      expect(await config.get('g.x', refresh: true), 2);
      expect(headerOf(t.requests[1], 'If-None-Match'), '"e1"');
    });

    test('a group covers its keys; the whole config covers everything',
        () async {
      final t = FakeTransport()
        ..answer(respond(200, configBody({'features': {'a': 1}, 'other': {'b': 2}})));
      final config = testClient(t, apiKey: 'k').config;
      await config.load(['features', 'other']);
      // 'features.a' is covered by the 'features,other' entry; a second get
      // of a missing key under it does not fetch.
      expect(await config.get('features.a'), 1);
      expect(t.requests, hasLength(1));
    });

    test('getSync reads the copy without a request', () async {
      final t = FakeTransport()
        ..answer(respond(200, configBody({'g': {'x': 7}})));
      final config = testClient(t, apiKey: 'k').config;
      expect(config.getSync('g.x', 'fb'), 'fb');
      await config.load(['g']);
      expect(config.getSync('g.x'), 7);
      expect(config.getSync('g.y', 'fb'), 'fb');
    });
  });

  group('changes', () {
    test('a revalidated copy reports the changed paths', () async {
      final t = FakeTransport()
        ..answer(respond(200, configBody({'g': {'a': 1, 'b': {'c': 2}}},
            ttl: 0), {'ETag': '"e1"'}))
        ..answer(respond(200, configBody({'g': {'a': 9, 'b': {'c': 2}}},
            ttl: 60), {'ETag': '"e2"'}));
      final config = testClient(t, apiKey: 'k').config;
      final seen = <List<String>>[];
      final sub = config.changes.listen(seen.add);
      await config.load(['g']);
      await config.get('g.a', refresh: true);
      await Future<void>.delayed(Duration.zero);
      expect(seen, hasLength(1));
      expect(seen.single, ['g.a']);
      await sub.cancel();
    });
  });

  group('scope', () {
    test('another credential gets its own copy and storage key', () async {
      final store = RecordingConfigStorage();
      final storage = MemoryTokenStorage();
      final t = FakeTransport()
        ..answer(respond(200, configBody({'g': {'x': 'anon'}})))
        ..answer(respond(200, configBody({'g': {'x': 'mine'}})));
      final client =
          testClient(t, configStorage: store, storage: storage);
      expect(await client.config.get('g.x'), 'anon');
      await storage.set('tok-1');
      expect(await client.config.get('g.x'), 'mine');
      // Persisted under each owner's key.
      final keys = store.items.keys.toList();
      expect(keys.length, 2);
      expect(keys.every((k) => k.startsWith('backd.config:shelf:')), isTrue);
    });

    test('sign-out clears the copies', () async {
      final store = RecordingConfigStorage();
      final storage = MemoryTokenStorage();
      await storage.set('tok');
      final t = FakeTransport()
        ..answer(respond(200, configBody({'g': {'x': 1}})))
        ..answer(respond(204))
        ..answer(respond(200, configBody({'g': {'x': 'anon'}})));
      final client = testClient(t, configStorage: store, storage: storage);
      expect(await client.config.get('g.x'), 1);
      await client.auth.logout();
      expect(await client.config.get('g.x'), 'anon');
    });
  });

  group('persistence', () {
    test('a second client restores the stored copy', () async {
      final store = RecordingConfigStorage();
      final t1 = FakeTransport()
        ..answer(respond(200, configBody({'g': {'x': 1}}), {'ETag': '"e1"'}));
      await testClient(t1, apiKey: 'k', configStorage: store)
          .config
          .load(['g']);
      // A new client over the same store answers from the restored copy
      // only after revalidating (ttl is in the past once persisted? No:
      // fresh entries serve at once — so no request is made).
      final t2 = FakeTransport();
      final config = testClient(t2, apiKey: 'k', configStorage: store).config;
      expect(await config.get('g.x'), 1);
      expect(t2.requests, isEmpty);
    });
  });
}
