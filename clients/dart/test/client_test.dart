import 'dart:convert';

import 'package:backd/backd.dart';
import 'package:test/test.dart';

import 'helpers.dart';

void main() {
  group('construction', () {
    test('requires url and realm', () {
      expect(() => BackdClient<Object?>(url: '', realm: 'r'), throwsArgumentError);
      expect(() => BackdClient<Object?>(url: 'http://h', realm: ''), throwsArgumentError);
    });

    test('trailing slashes on the url are trimmed', () {
      final t = FakeTransport();
      expect(
        BackdClient<Object?>(url: 'http://h///', realm: 'r', transport: t.call).url,
        'http://h',
      );
      expect(testClient(t, apiKey: 'k').url, 'http://backd.test');
    });

    test('hasApiKey and close() on a closed client', () {
      final client = BackdClient<Object?>(url: 'http://h', realm: 'r');
      expect(client.hasApiKey, isFalse);
      client.close();
      expect(
        () => client.request(
          const RequestInit(method: 'GET', path: ['a', 'b']),
        ),
        throwsStateError,
      );
    });
  });

  group('request pipeline', () {
    test('builds /v1/{realm} urls with encoded segments and query', () async {
      final t = FakeTransport()..answer(respond(200, {'items': <Object?>[]}));
      await testClient(t, apiKey: 'k').request(
        const RequestInit(
          method: 'GET',
          path: ['main', 'my posts', 'a b'],
          query: {'limit': 20, 'tag': 'a&b', 'skip': null},
        ),
      );
      final uri = t.requests.single.url;
      expect(uri.path, '/v1/shelf/main/my%20posts/a%20b');
      expect(uri.queryParameters['limit'], '20');
      expect(uri.queryParameters['tag'], 'a&b');
      expect(uri.queryParameters.containsKey('skip'), isFalse);
    });

    test('JSON body and default Accept/Content-Type', () async {
      final t = FakeTransport()..answer(respond(201, {'id': 'x'}));
      final res = await testClient(t, apiKey: 'k').request(
        const RequestInit(
          method: 'POST',
          path: ['main', 'posts'],
          body: {'title': 'hi'},
        ),
      );
      final req = t.requests.single;
      expect(headerOf(req, 'Accept'), 'application/json');
      expect(headerOf(req, 'Content-Type'), 'application/json');
      expect(jsonDecode(utf8.decode(req.body!)), {'title': 'hi'});
      expect((res.data as Map)['id'], 'x');
    });

    test('rawBody goes out as-is with octet-stream', () async {
      final t = FakeTransport()..answer(respond(200, {'id': 'f1'}));
      await testClient(t, apiKey: 'k').request(
        RequestInit(
          method: 'PUT',
          path: ['main', 'docs', 'd1', '_file', 'body'],
          rawBody: utf8.encode('bytes'),
        ),
      );
      final req = t.requests.single;
      expect(headerOf(req, 'Content-Type'), 'application/octet-stream');
      expect(utf8.decode(req.body!), 'bytes');
    });

    test('raw returns the response unread', () async {
      final t = FakeTransport()
        ..answer(respond(200, 'BIN', {'Content-Type': 'application/octet-stream'}));
      final res = await testClient(t, apiKey: 'k').request(
        const RequestInit(method: 'GET', path: ['files', 'f1'], raw: true),
      );
      expect(headerOf(t.requests.single, 'Accept'), '*/*');
      expect(res.data, isNull);
      expect(res.response!.bodyText, 'BIN');
    });

    test('empty 2xx answers have null data', () async {
      final t = FakeTransport()..answer(respond(204));
      final res = await testClient(t, apiKey: 'k').request(
        const RequestInit(method: 'DELETE', path: ['main', 'posts', 'x']),
      );
      expect(res.data, isNull);
    });

    test('client headers merge, request headers win', () async {
      final t = FakeTransport()..answer(respond(200, {}));
      await testClient(t, apiKey: 'k', headers: {'X-App': 'shelf', 'X-One': 'c'})
          .request(
        const RequestInit(
          method: 'GET',
          path: ['main', 'posts'],
          headers: {'X-One': 'r'},
        ),
      );
      expect(headerOf(t.requests.single, 'X-App'), 'shelf');
      expect(headerOf(t.requests.single, 'X-One'), 'r');
    });

    test('apiKey becomes a Bearer token; storage token does too', () async {
      final t = FakeTransport()
        ..answer(respond(200, {}))
        ..answer(respond(200, {}))
        ..answer(respond(200, {}));
      final storage = MemoryTokenStorage();
      await storage.set('sess-1');
      final keyClient = testClient(t, apiKey: 'bdk_secret');
      final sessClient = testClient(t, storage: storage);
      final anonClient = testClient(t);
      await keyClient.request(
        const RequestInit(method: 'GET', path: ['main', 'posts']),
      );
      await sessClient.request(
        const RequestInit(method: 'GET', path: ['main', 'posts']),
      );
      await anonClient.request(
        const RequestInit(method: 'GET', path: ['main', 'posts']),
      );
      expect(headerOf(t.requests[0], 'Authorization'), 'Bearer bdk_secret');
      expect(headerOf(t.requests[1], 'Authorization'), 'Bearer sess-1');
      expect(headerOf(t.requests[2], 'Authorization'), isNull);
    });

    test('auth: false sends no credentials', () async {
      final t = FakeTransport()..answer(respond(200, {}));
      await testClient(t, apiKey: 'k').request(
        const RequestInit(method: 'GET', path: ['main', 'posts'], auth: false),
      );
      expect(headerOf(t.requests.single, 'Authorization'), isNull);
    });

    test('reauth sends X-Backd-Reauth while the proof is good', () async {
      final t = FakeTransport()
        ..answer(respond(200, {}))
        ..answer(respond(200, {}));
      final client = testClient(t, apiKey: 'k')
        ..proof = ReauthProof(
          token: 'proof-1',
          expires: DateTime.now().add(const Duration(minutes: 5)),
        );
      await client.request(
        const RequestInit(method: 'POST', path: ['_auth', 'x'], reauth: true),
      );
      expect(headerOf(t.requests[0], 'X-Backd-Reauth'), 'proof-1');
      client.proof = ReauthProof(
        token: 'proof-2',
        expires: DateTime.now().subtract(const Duration(minutes: 1)),
      );
      await client.request(
        const RequestInit(method: 'POST', path: ['_auth', 'x'], reauth: true),
      );
      expect(headerOf(t.requests[1], 'X-Backd-Reauth'), isNull);
    });

    test('network failures become NetworkError with the cause', () async {
      final t = FakeTransport()..fail(StateError('broken pipe'));
      expect(
        testClient(t, apiKey: 'k').request(
          const RequestInit(method: 'GET', path: ['main', 'posts']),
        ),
        throwsA(
          isA<NetworkError>()
              .having((e) => e.status, 'status', 0)
              .having((e) => e.code, 'code', 'network_error')
              .having((e) => e.cause, 'cause', isA<StateError>()),
        ),
      );
    });
  });

  group('types fingerprint', () {
    test('X-Backd-Types is sent when configured, never otherwise', () async {
      final t = FakeTransport()
        ..answer(respond(200, {}))
        ..answer(respond(200, {}));
      await testClient(t, apiKey: 'k', types: 'fp-1').request(
        const RequestInit(method: 'GET', path: ['main', 'posts']),
      );
      await testClient(t, apiKey: 'k').request(
        const RequestInit(method: 'GET', path: ['main', 'posts']),
      );
      expect(headerOf(t.requests[0], 'X-Backd-Types'), 'fp-1');
      expect(headerOf(t.requests[1], 'X-Backd-Types'), isNull);
    });

    test('X-Backd-Types-Stale calls onStaleTypes once', () async {
      var calls = 0;
      final t = FakeTransport()
        ..answer(respond(200, {}, {'X-Backd-Types-Stale': 'true'}))
        ..answer(respond(200, {}, {'X-Backd-Types-Stale': 'true'}));
      final client = testClient(
        t,
        apiKey: 'k',
        types: 'fp-1',
        onStaleTypes: () => calls++,
      );
      await client.request(
        const RequestInit(method: 'GET', path: ['main', 'posts']),
      );
      await client.request(
        const RequestInit(method: 'GET', path: ['main', 'posts']),
      );
      expect(calls, 1);
    });
  });

  group('retries', () {
    test('GET retries 429 and 503 up to attempts', () async {
      final t = FakeTransport()
        ..answer(respond(429, {'error': {'code': 'rate_limited', 'message': 'slow down'}}))
        ..answer(respond(503, {'error': {'code': 'busy', 'message': 'later'}},
            {'Retry-After': '2'}))
        ..answer(respond(200, {'items': <Object?>[]}));
      final res = await testClient(
        t,
        apiKey: 'k',
        retry: const RetryOptions(attempts: 2),
      ).request(const RequestInit(method: 'GET', path: ['main', 'posts']));
      expect(t.requests, hasLength(3));
      expect((res.data as Map)['items'], isEmpty);
    });

    test('attempts exhausted: the RetryableError surfaces', () async {
      final t = FakeTransport()
        ..answer(respond(429, {'error': {'code': 'rate_limited', 'message': 'x'}}))
        ..answer(respond(429, {'error': {'code': 'rate_limited', 'message': 'x'}},
            {'Retry-After': '3'}));
      await expectLater(
        testClient(t, apiKey: 'k', retry: const RetryOptions(attempts: 1))
            .request(const RequestInit(method: 'GET', path: ['main', 'posts'])),
        throwsA(
          isA<RetryableError>()
              .having((e) => e.retryAfter, 'retryAfter', const Duration(seconds: 3)),
        ),
      );
      expect(t.requests, hasLength(2));
    });

    test('unsafe requests are not retried', () async {
      final t = FakeTransport()
        ..answer(respond(503, {'error': {'code': 'busy', 'message': 'x'}}));
      await expectLater(
        testClient(t, apiKey: 'k', retry: const RetryOptions(attempts: 3))
            .request(const RequestInit(method: 'POST', path: ['main', 'posts'])),
        throwsA(isA<RetryableError>()),
      );
      expect(t.requests, hasLength(1));
    });

    test('If-Match or Idempotency-Key make a write retriable', () async {
      for (final header in ['If-Match', 'Idempotency-Key']) {
        final t = FakeTransport()
          ..answer(respond(503, {'error': {'code': 'busy', 'message': 'x'}}))
          ..answer(respond(200, {'ok': true}));
        await testClient(t, apiKey: 'k', retry: const RetryOptions(attempts: 1))
            .request(RequestInit(
          method: 'PATCH',
          path: const ['main', 'posts', 'p1'],
          body: const {},
          headers: {header: '"v1"'},
        ));
        expect(t.requests, hasLength(2), reason: header);
      }
    });

    test('per-request retry overrides the client', () async {
      final t = FakeTransport()
        ..answer(respond(429, {'error': {'code': 'rate_limited', 'message': 'x'}}));
      await expectLater(
        testClient(t, apiKey: 'k', retry: const RetryOptions(attempts: 5))
            .request(
          const RequestInit(
            method: 'GET',
            path: ['main', 'posts'],
            retry: RetryOptions(attempts: 0),
          ),
        ),
        throwsA(isA<RetryableError>()),
      );
      expect(t.requests, hasLength(1));
    });
  });

  group('revalidation cache', () {
    const path = ['main', 'posts'];
    final body = {
      'items': [
        {'title': 'a'},
      ],
    };

    test('second identical read sends If-None-Match; 304 replays the body',
        () async {
      final t = FakeTransport()
        ..answer(respond(200, body, {'ETag': '"v1"'}))
        ..answer(respond(304));
      final client = testClient(t, apiKey: 'k');
      final first = await client.request(
        const RequestInit(method: 'GET', path: path),
      );
      expect(first.fromCache, isFalse);
      final second = await client.request(
        const RequestInit(method: 'GET', path: path),
      );
      expect(headerOf(t.requests[1], 'If-None-Match'), '"v1"');
      expect(second.fromCache, isTrue);
      expect(second.data, body);
      final stats = client.cacheStats();
      expect(stats.hits, 1);
      expect(stats.misses, 1);
    });

    test('writes to a document drop its cached reads', () async {
      final t = FakeTransport()
        ..answer(respond(200, body, {'ETag': '"v1"'})) // GET doc
        ..answer(respond(200, {})) // PATCH doc
        ..answer(respond(200, body, {'ETag': '"v2"'})); // GET doc again
      final client = testClient(t, apiKey: 'k');
      const docPath = ['main', 'posts', 'p1'];
      await client.request(const RequestInit(method: 'GET', path: docPath));
      await client.request(
        const RequestInit(method: 'PATCH', path: docPath, body: {'x': 1}),
      );
      await client.request(const RequestInit(method: 'GET', path: docPath));
      // No If-None-Match on the re-read: the write dropped the entry.
      expect(headerOf(t.requests[2], 'If-None-Match'), isNull);
    });

    test('cache: false on the request skips the cache', () async {
      final t = FakeTransport()
        ..answer(respond(200, body, {'ETag': '"v1"'}))
        ..answer(respond(200, body, {'ETag': '"v1"'}));
      final client = testClient(t, apiKey: 'k');
      await client.request(const RequestInit(method: 'GET', path: path));
      await client.request(
        const RequestInit(method: 'GET', path: path, cache: false),
      );
      expect(headerOf(t.requests[1], 'If-None-Match'), isNull);
    });

    test('CacheOptions(enabled: false) disables the cache', () async {
      final t = FakeTransport()
        ..answer(respond(200, body, {'ETag': '"v1"'}))
        ..answer(respond(200, body, {'ETag': '"v1"'}));
      final client =
          testClient(t, apiKey: 'k', cache: const CacheOptions(enabled: false));
      await client.request(const RequestInit(method: 'GET', path: path));
      await client.request(const RequestInit(method: 'GET', path: path));
      expect(headerOf(t.requests[1], 'If-None-Match'), isNull);
      expect(client.cacheStats().entries, 0);
    });

    test('a caller-owned If-None-Match passes the 304 through', () async {
      final t = FakeTransport()
        ..answer(respond(200, body, {'ETag': '"v1"'}))
        ..answer(respond(304));
      final client = testClient(t, apiKey: 'k');
      await client.request(const RequestInit(method: 'GET', path: path));
      final res = await client.request(const RequestInit(
        method: 'GET',
        path: path,
        headers: {'If-None-Match': '"mine"'},
      ));
      expect(res.status, 304);
      expect(res.data, isNull);
      // The caller's tag was sent, not the cached one.
      expect(headerOf(t.requests[1], 'If-None-Match'), '"mine"');
    });

    test('reads under different credentials never share entries', () async {
      // One client whose storage token changes scopes the cache: a read as
      // another caller must not send the previous caller's ETag.
      final TokenStorage storage = MemoryTokenStorage();
      await storage.set('tok-a');
      final t = FakeTransport()
        ..answer(respond(200, body, {'ETag': '"v1"'})) // read as tok-a
        ..answer(respond(200, body, {'ETag': '"v9"'})); // read as tok-b
      final client = testClient(t, storage: storage);
      await client.request(const RequestInit(method: 'GET', path: path));
      await storage.set('tok-b');
      await client.request(const RequestInit(method: 'GET', path: path));
      expect(headerOf(t.requests[1], 'If-None-Match'), isNull);
    });
  });

  group('session expiry', () {
    test('unauthenticated with a sent session fires onSessionExpired', () async {
      final t = FakeTransport()..answer(respond(401, {
          'error': {'code': 'unauthenticated', 'message': 'expired'},
        }));
      var expired = 0;
      final storage = MemoryTokenStorage();
      await storage.set('tok');
      final client = testClient(t, storage: storage)
        ..onSessionExpired = () => expired++;
      await expectLater(
        client.request(const RequestInit(method: 'GET', path: ['main', 'posts'])),
        throwsA(isA<AuthenticationError>()),
      );
      expect(expired, 1);
    });

    test('unauthenticated without a sent session does not', () async {
      final t = FakeTransport()..answer(respond(401, {
          'error': {'code': 'unauthenticated', 'message': 'expired'},
        }));
      var expired = 0;
      final client = testClient(t, apiKey: 'k')
        ..onSessionExpired = () => expired++;
      await expectLater(
        client.request(const RequestInit(method: 'GET', path: ['main', 'posts'])),
        throwsA(isA<AuthenticationError>()),
      );
      expect(expired, 0);
    });
  });
}
