import 'dart:convert';

import 'package:backd/backd.dart';
import 'package:test/test.dart';

import 'helpers.dart';

const queued = {'id': 'e1', 'status': 'queued', 'created_at': '2026-10-24T10:00:00.000Z'};
const ready = {
  'id': 'e1',
  'status': 'ready',
  'created_at': '2026-10-24T10:00:00.000Z',
  'expires_at': '2026-10-31T10:00:00.000Z',
  'size': 12,
  'counts': {'files': 0},
};

void main() {
  test('requestExport sends the fresh proof the client holds', () async {
    final t = FakeTransport()
      ..answer(respond(200, {
        'reauth_token': 'rt-1',
        'expires_at': DateTime.now().add(const Duration(minutes: 1)).toUtc().toIso8601String(),
      }))
      ..answer(respond(202, queued));
    final c = testClient(t, apiKey: 'k');
    await c.auth.reauth('password', password: 'pw');
    expect(await c.account.requestExport(), queued);
    expect(t.requests[1].method, 'POST');
    expect(t.requests[1].url.path, '/v1/shelf/_auth/me/export');
    expect(headerOf(t.requests[1], 'X-Backd-Reauth'), 'rt-1');
  });

  test('asking twice in a day is a retryable error that says how long', () async {
    final t = FakeTransport()
      ..answer(respond(429, {'error': {'code': 'too_many_requests', 'message': 'later'}}, {'Retry-After': '82800'}));
    final c = testClient(t, apiKey: 'k');
    await expectLater(
        c.account.requestExport(),
        throwsA(isA<RetryableError>()
            .having((e) => e.status, 'status', 429)
            .having((e) => e.retryAfter, 'retryAfter', const Duration(seconds: 82800))));
  });

  test('exports lists, export reads one and waitForExport follows it', () async {
    final running = {...queued, 'status': 'running', 'step': 'Export: documents app/orders'};
    final t = FakeTransport()
      ..answer(respond(200, {'items': [ready]}))
      ..answer(respond(200, running))
      ..answer(respond(200, running))
      ..answer(respond(200, ready));
    final c = testClient(t, apiKey: 'k');
    expect(await c.account.exports(), [ready]);
    expect(t.requests[0].url.path, '/v1/shelf/_auth/me/exports');
    expect((await c.account.export('e1'))['step'], 'Export: documents app/orders');
    final seen = <Object?>[];
    final done = await c.account.waitForExport('e1',
        pollInterval: const Duration(milliseconds: 1), onProgress: (e) => seen.add(e['status']));
    expect(done['status'], 'ready');
    expect(seen, ['running', 'ready']);
  });

  test('waitForExport stops on a failed or expired export, and on its timeout', () async {
    var t = FakeTransport()..answer(respond(200, {...queued, 'status': 'failed'}));
    await expectLater(
        testClient(t, apiKey: 'k').account.waitForExport('e1', pollInterval: const Duration(milliseconds: 1)),
        throwsA(isA<StateError>().having((e) => e.message, 'message', contains('ask for a new one'))));
    t = FakeTransport()..answer(respond(200, {...queued, 'status': 'expired'}));
    await expectLater(
        testClient(t, apiKey: 'k').account.waitForExport('e1', pollInterval: const Duration(milliseconds: 1)),
        throwsA(isA<StateError>().having((e) => e.message, 'message', contains('expired'))));
    t = FakeTransport()
      ..answer(respond(200, queued))
      ..answer(respond(200, queued))
      ..answer(respond(200, queued));
    await expectLater(
        testClient(t, apiKey: 'k').account.waitForExport('e1',
            pollInterval: const Duration(milliseconds: 5), timeout: const Duration(milliseconds: 8)),
        throwsA(isA<StateError>().having((e) => e.message, 'message', contains('still queued'))));
  });

  test('downloadExport gives the signed link, and its bytes from it', () async {
    final t = FakeTransport()
      ..answer(respond(200, {'url': 'http://storage.test/pkg.zip?sig=1', 'expires_at': '2026-10-24T10:05:00.000Z'},
          {'Content-Type': 'application/json'}))
      ..answer(TransportResponse(status: 200, body: const [80, 75, 3, 4]));
    final c = testClient(t, apiKey: 'k');
    final d = await c.account.downloadExport('e1');
    expect(d.url, 'http://storage.test/pkg.zip?sig=1');
    expect(t.requests[0].url.path, '/v1/shelf/_auth/me/exports/e1/download');
    expect(headerOf(t.requests[0], 'Accept'), 'application/json');
    expect(await d.bytes(), [80, 75, 3, 4]);
    expect(t.requests[1].url.host, 'storage.test');
    expect(headerOf(t.requests[1], 'Authorization'), isNull); // the link is signed
  });

  test('a realm that proxies its files sends the zip itself', () async {
    final t = FakeTransport()
      ..answer(TransportResponse(
          status: 200, headers: {'content-type': ['application/zip']}, body: utf8.encode('PK-package')));
    final d = await testClient(t, apiKey: 'k').account.downloadExport('e1');
    expect(d.url, isNull);
    expect(utf8.decode(await d.bytes()), 'PK-package');
  });

  test('a package that is not ready, or is gone, is a typed error', () async {
    final t = FakeTransport()
      ..answer(respond(409, {'error': {'code': 'conflict', 'message': 'queued'}}))
      ..answer(respond(410, {'error': {'code': 'gone', 'message': 'expired'}}));
    final c = testClient(t, apiKey: 'k');
    await expectLater(c.account.downloadExport('e1'), throwsA(isA<BackdException>().having((e) => e.status, 'status', 409)));
    await expectLater(c.account.downloadExport('e1'), throwsA(isA<BackdException>().having((e) => e.status, 'status', 410)));
  });
}
