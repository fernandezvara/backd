import 'dart:async';
import 'dart:convert';

import 'package:backd/backd.dart';
import 'package:test/test.dart';

import 'helpers.dart';

String sse(Map<String, Object?> ev) {
  final buf = StringBuffer();
  if (ev['id'] != null) buf.write('id: ${ev['id']}\n');
  buf.write('event: ${ev['event']}\n');
  if (ev['data'] != null) buf.write('data: ${jsonEncode(ev['data'])}\n');
  buf.write('\n');
  return buf.toString();
}

Map<String, Object?> doc(String id, [int version = 1]) =>
    {'id': id, '_meta': {'version': version}};

/// Waits until [cond] holds or [timeout] passes.
Future<void> until(bool Function() cond,
    {Duration timeout = const Duration(seconds: 2)}) async {
  final end = DateTime.now().add(timeout);
  while (!cond() && DateTime.now().isBefore(end)) {
    await Future<void>.delayed(const Duration(milliseconds: 1));
  }
  if (!cond()) throw StateError('until: timed out');
}

void main() {
  configLiveTests();

  test('subscribe requires specs', () {
    final c = testClient(FakeTransport(), streamTransport: FakeStreamTransport());
    expect(() => c.realtime.subscribe([]), throwsArgumentError);
    expect(
        () => c.realtime
            .subscribe([const SubscriptionSpec(db: '', collection: 'c')]),
        throwsArgumentError);
  });

  test('opens the stream, loads the list, goes live', () async {
    final t = FakeTransport()
      // The initial list load.
      ..answer(respond(200, {
        'items': [doc('a'), doc('b')],
        'has_more': false,
      }));
    final ctl = StreamController<List<int>>();
    final st = FakeStreamTransport()..controlled(ctl.stream);
    final c = testClient(t, apiKey: 'k', streamTransport: st);
    final statuses = <LiveStatus>[];
    final live = c.realtime.subscribe([
      const SubscriptionSpec(db: 'main', collection: 'items'),
    ])
      ..on('status', (s) => statuses.add(s as LiveStatus));
    ctl.add(utf8.encode(sse({'id': 't1', 'event': 'ready', 'data': {}})));
    final state = await live.ready.timeout(const Duration(seconds: 5));
    expect(state.status, LiveStatus.live);
    expect(state.items.map((d) => d['id']), ['a', 'b']);
    expect(st.requests.single.url.path, '/v1/shelf/_realtime');
    final body = jsonDecode(utf8.decode(st.requests.single.body!)) as Map;
    expect((body['subscriptions'] as List).single['collection'], 'items');
    expect(statuses, [LiveStatus.live]);
    live.close();
    await ctl.close();
    expect(c.realtime.open, 0);
  });

  test('created/updated/removed keep the list in order', () async {
    final t = FakeTransport()
      ..answer(respond(200, {
        'items': [doc('a', 1), doc('c', 1)],
        'has_more': false,
      }));
    final ctl = StreamController<List<int>>();
    final st = FakeStreamTransport()..controlled(ctl.stream);
    final c = testClient(t, apiKey: 'k', streamTransport: st);
    final live = c.realtime.subscribe([
      const SubscriptionSpec(db: 'main', collection: 'items'),
    ]);
    ctl.add(utf8.encode(sse({'id': 't1', 'event': 'ready', 'data': {}}) +
        sse({
          'id': 't2',
          'event': 'created',
          'data': {'sub': 's1', 'doc': doc('b')},
        }) +
        sse({
          'id': 't3',
          'event': 'updated',
          'data': {'sub': 's1', 'doc': doc('a', 2)},
        }) +
        sse({
          'id': 't4',
          'event': 'removed',
          'data': {'sub': 's1', 'id': 'c', 'version': 2},
        })));
    await live.ready.timeout(const Duration(seconds: 5));
    await until(() =>
        live.items.length == 2 &&
        (live.items.first['_meta'] as Map)['version'] == 2);
    expect(live.items.map((d) => d['id']), ['a', 'b']);
    live.close();
    await ctl.close();
  });

  test('sort orders the live list by the declared key', () async {
    final t = FakeTransport()
      ..answer(respond(200, {
        'items': [
          {'id': 'x', 'pos': 2, '_meta': {'version': 1}},
          {'id': 'y', 'pos': 1, '_meta': {'version': 1}},
        ],
        'has_more': false,
      }));
    final ctl = StreamController<List<int>>();
    final st = FakeStreamTransport()..controlled(ctl.stream);
    final c = testClient(t, apiKey: 'k', streamTransport: st);
    final live = c.realtime.subscribe([
      const SubscriptionSpec(db: 'main', collection: 'items', sort: 'pos'),
    ]);
    ctl.add(utf8.encode(sse({'id': 't1', 'event': 'ready', 'data': {}})));
    await live.ready.timeout(const Duration(seconds: 5));
    await until(() => live.items.length == 2);
    expect(live.items.map((d) => d['id']), ['y', 'x']);
    live.close();
    await ctl.close();
  });

  test('a reset reloads with jitter and keeps the list meanwhile', () async {
    final t = FakeTransport()
      // Initial load.
      ..answer(respond(200, {
        'items': [doc('a')],
        'has_more': false,
      }))
      // The reload after the reset.
      ..answer(respond(200, {
        'items': [doc('a'), doc('b')],
        'has_more': false,
      }));
    final ctl = StreamController<List<int>>();
    final st = FakeStreamTransport()..controlled(ctl.stream);
    final c = testClient(t, apiKey: 'k', streamTransport: st);
    final resets = <Object?>[];
    final live = c.realtime
        .subscribe([const SubscriptionSpec(db: 'main', collection: 'items')],
            random: () => 0.0)
      ..on('reset', (r) => resets.add(r));
    ctl.add(utf8.encode(sse({'id': 't1', 'event': 'ready', 'data': {}}) +
        sse({
          'id': 't2',
          'event': 'reset',
          'data': {
            'subs': ['s1'],
            'reason': 'config'
          },
        })));
    await live.ready.timeout(const Duration(seconds: 5));
    await until(() => live.items.length == 2);
    expect(resets.single, {'subscription': '0', 'reason': 'config'});
    live.close();
    await ctl.close();
  });

  test('close stops the view and the stream reconciles', () async {
    final t = FakeTransport()
      ..answer(respond(200, {'items': <Object?>[], 'has_more': false}));
    final ctl = StreamController<List<int>>();
    final st = FakeStreamTransport()..controlled(ctl.stream);
    final c = testClient(t, apiKey: 'k', streamTransport: st);
    final live = c.realtime.subscribe([
      const SubscriptionSpec(db: 'main', collection: 'items'),
    ]);
    ctl.add(utf8.encode(sse({'id': 't1', 'event': 'ready', 'data': {}})));
    await live.ready.timeout(const Duration(seconds: 5));
    live.close();
    live.close(); // idempotent
    expect(live.state.status, LiveStatus.closed);
    expect(c.realtime.open, 0);
    await ctl.close();
  });

  test('a stream that fails to open retries until closed', () async {
    final st = FakeStreamTransport()
      ..fail('socket')
      ..fail('socket')
      ..fail('socket');
    final t = FakeTransport();
    final c = testClient(t, apiKey: 'k', streamTransport: st);
    final live = c.realtime.subscribe(
        [const SubscriptionSpec(db: 'main', collection: 'items')],
        maxBackoff: Duration.zero);
    // Network errors retry forever with backoff 0; three attempts arrive.
    await until(() => st.requests.length >= 3,
        timeout: const Duration(seconds: 5));
    live.close();
  });

  test('session_ended fails every view with SessionEndedError', () async {
    final t = FakeTransport()
      ..answer(respond(200, {'items': <Object?>[], 'has_more': false}));
    final ctl = StreamController<List<int>>();
    final st = FakeStreamTransport()..controlled(ctl.stream);
    final c = testClient(t, apiKey: 'k', streamTransport: st);
    final live = c.realtime.subscribe([
      const SubscriptionSpec(db: 'main', collection: 'items'),
    ]);
    ctl.add(utf8.encode(sse({'id': 't1', 'event': 'ready', 'data': {}})));
    await live.ready.timeout(const Duration(seconds: 5));
    ctl.add(utf8.encode(sse({'event': 'session_ended', 'data': {}})));
    await until(() => live.closed);
    expect(live.error, isA<SessionEndedError>());
    await expectLater(live.ready, throwsA(isA<SessionEndedError>()));
    await ctl.close();
  });

  test('references are fetched in a batched read', () async {
    final t = FakeTransport()
      // Initial load: the items carry an `author` reference.
      ..answer(respond(200, {
        'items': [
          {'id': 'a', 'author': 'u1', '_meta': {'version': 1}},
        ],
        'has_more': false,
      }))
      // The batched reference read.
      ..answer(respond(200, {
        'items': [
          {'id': 'u1', 'name': 'Ana', 'secret': 'x'},
        ],
        'has_more': false,
      }));
    final ctl = StreamController<List<int>>();
    final st = FakeStreamTransport()..controlled(ctl.stream);
    final c = testClient(t, apiKey: 'k', streamTransport: st);
    final live = c.realtime.subscribe([
      const SubscriptionSpec(
        db: 'main',
        collection: 'items',
        references: {
          'author':
              ReferenceSpec(field: 'author', collection: 'users', fields: ['name']),
        },
      ),
    ]);
    ctl.add(utf8.encode(sse({'id': 't1', 'event': 'ready', 'data': {}})));
    await live.ready.timeout(const Duration(seconds: 5));
    await until(() => (live.state.references['author']?.length ?? 0) == 1);
    expect(live.state.references['author']?['u1']?['name'], 'Ana');
    // Only id, _meta and the named fields are kept.
    expect(live.state.references['author']?['u1']?.containsKey('secret'),
        isFalse);
    live.close();
    await ctl.close();
  });
}

void configLiveTests() {
  group('config.live', () {
    test('adds a _config subscription; config_changed rereads the branch held',
        () async {
      final t = FakeTransport()
        ..answer(respond(
            200,
            {
              'values': {
                'maintenance': {'enabled': false},
              },
              'version': 'c1',
              'cache_ttl': 300,
            },
            {'ETag': 'W/"c1"'}))
        // The stream opens: nothing moved while it was not.
        ..answer(respond(304, {}))
        ..answer(respond(
            200,
            {
              'values': {
                'maintenance': {'enabled': true},
              },
              'version': 'c2',
              'cache_ttl': 300,
            },
            {'ETag': 'W/"c2"'}));
      final ctl = StreamController<List<int>>();
      final st = FakeStreamTransport()..controlled(ctl.stream);
      final c = testClient(t, apiKey: 'k', streamTransport: st);
      await c.config.load(['maintenance']);
      final changes = <List<String>>[];
      final sub = c.config.changes.listen(changes.add);
      final stop = c.config.live(['maintenance']);
      await until(() => st.requests.isNotEmpty);
      final body = jsonDecode(utf8.decode(st.requests.single.body!)) as Map;
      expect(body['subscriptions'], [
        {
          'id': '_config',
          'config': {
            'paths': ['maintenance']
          },
        },
      ]);
      ctl.add(utf8.encode(sse({
        'event': 'ready',
        'data': {
          'subscriptions': ['_config']
        }
      })));
      ctl.add(utf8.encode(sse({
        'event': 'config_changed',
        'data': {
          'id': '_config',
          'version': 'c2',
          'paths': ['maintenance.enabled']
        },
      })));
      await until(() => changes.isNotEmpty);
      expect(changes.single, ['maintenance.enabled']);
      expect(c.config.getSync('maintenance.enabled'), isTrue);
      expect(headerOf(t.requests.last, 'If-None-Match'), 'W/"c1"');
      stop();
      stop(); // twice is fine
      await sub.cancel();
      await ctl.close();
    });

    test('a change that is none of a held branch costs no request', () async {
      final t = FakeTransport()
        ..answer(respond(
            200,
            {
              'values': {
                'maintenance': {'enabled': false},
              },
              'version': 'c1',
              'cache_ttl': 300,
            },
            {'ETag': 'W/"c1"'}))
        ..answer(respond(304, {}));
      final ctl = StreamController<List<int>>();
      final st = FakeStreamTransport()..controlled(ctl.stream);
      final c = testClient(t, apiKey: 'k', streamTransport: st);
      await c.config.load(['maintenance']);
      final stop = c.config.live();
      await until(() => st.requests.isNotEmpty);
      ctl.add(utf8.encode(sse({'event': 'ready', 'data': {}})));
      ctl.add(utf8.encode(sse({
        'event': 'config_changed',
        'data': {
          'id': '_config',
          'version': 'c2',
          'paths': ['features.export.enabled']
        },
      })));
      await Future<void>.delayed(const Duration(milliseconds: 100));
      // The load and the check when the stream opened; nothing for the change.
      expect(t.requests, hasLength(2));
      stop();
      await ctl.close();
    });

    test('a refusal of the subscription reaches errors and ends live',
        () async {
      final t = FakeTransport();
      final st = FakeStreamTransport()
        ..fail(ValidationError(
          status: 400,
          code: 'invalid_query',
          message: 'invalid subscription',
          details: [
            const ErrorDetail(
                path: 'subscriptions[0].config',
                reason: 'this realm declares no app configuration')
          ],
        ));
      final c = testClient(t, apiKey: 'k', streamTransport: st);
      final errors = <Object>[];
      final sub = c.config.errors.listen(errors.add);
      c.config.live();
      await until(() => errors.isNotEmpty);
      expect(errors.single, isA<ValidationError>());
      await sub.cancel();
    });
  });
}
