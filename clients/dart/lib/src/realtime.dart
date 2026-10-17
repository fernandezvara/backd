import 'dart:async';
import 'dart:convert';
import 'dart:math' show Random;

import 'client.dart';
import 'errors.dart';
import 'transport.dart';

/// `connecting`: the stream is opening (or reopening) and nothing has been
/// loaded yet; `live`: loaded and following; `resyncing`: reloading after a
/// reset or a reconnection, while the last list stays available; `closed`:
/// stopped, by `close()` or by an error.
enum LiveStatus { connecting, live, resyncing, closed }

/// A reference the list's documents carry: the field holds the referenced
/// document's id (or an array of ids).
final class ReferenceSpec {
  const ReferenceSpec({required this.field, required this.collection,
      this.db, this.fields});

  /// A field of the listed documents (a dot path).
  final String field;

  /// The referenced collection's database; default the subscription's.
  final String? db;
  final String collection;

  /// Keep only these fields of each referenced document (and its `id`,
  /// `_meta`).
  final List<String>? fields;
}

/// What to follow, and how to load it first.
final class SubscriptionSpec {
  const SubscriptionSpec({
    this.id,
    required this.db,
    required this.collection,
    this.where,
    this.doc,
    this.sort,
    this.limit,
    this.references,
  });

  /// Names the subscription in `state.subscriptions`; default its
  /// position.
  final String? id;

  /// The database.
  final String db;

  /// A collection that declares `realtime: true`.
  final String collection;

  /// The filter, in the query language limited to comparisons, `$in`,
  /// `$nin`, `$between` and `$isNull` with `$and`/`$or`. Subscribe with an
  /// equality on a grouping field (`_meta.org`) to keep the server's work
  /// small.
  final Map<String, Object?>? where;

  /// Follow one document by id instead of a filter.
  final String? doc;

  /// The initial list and how the live list is kept: `sort`
  /// (`'-_meta.created_at'`, default `id`) orders it, new documents are
  /// inserted in order, and `limit` (default 100) is the most it holds.
  final Object? sort;
  final int? limit;

  /// Referenced documents to keep beside the list, by name.
  final Map<String, ReferenceSpec>? references;
}

/// One subscription's documents and how far it got.
final class LiveSubscriptionState {
  const LiveSubscriptionState({required this.items, required this.status});

  /// The documents, in order.
  final List<Map<String, Object?>> items;
  final LiveStatus status;
}

/// The view's state, as `change` reports it.
final class LiveState {
  const LiveState({
    required this.status,
    required this.items,
    required this.subscriptions,
    required this.references,
  });

  final LiveStatus status;

  /// The first subscription's documents (the usual case: one).
  final List<Map<String, Object?>> items;

  /// Every subscription, by id.
  final Map<String, LiveSubscriptionState> subscriptions;

  /// Referenced documents: by reference name, then id.
  final Map<String, Map<String, Map<String, Object?>>> references;
}

const _reconnectBase = Duration(milliseconds: 500);

/// Live lists over one stream per client: the client multiplexes every
/// subscription of every view over `POST /v1/{realm}/_realtime`, loads each
/// list stream-first (events received while the list loads are applied
/// after it, ignoring versions that are not newer), keeps it sorted,
/// resumes after a disconnection, reloads subscriptions the server resets
/// (with jitter), reconnects at the end of the credential's lifetime and
/// reopens the stream when the set of subscriptions changes.
final class Realtime {
  Realtime(this.client);

  final BackdClient<Object?> client;

  final _subs = <String, _Sub>{};
  var _seq = 0;
  String? _token;
  var _gen = 0;
  StreamSubscription<List<int>>? _reader;
  var _syncing = false;
  var _ready = false;
  final _views = <LiveView>{};

  /// A source of randomness for the jitter after a reset.
  final _random = Random();

  /// Follows collections live. The returned view loads each subscription,
  /// keeps its list current and tells you when it changes:
  ///
  /// ```dart
  /// final live = client.realtime.subscribe([
  ///   SubscriptionSpec(
  ///     db: 'main', collection: 'items',
  ///     where: {'_meta.org': orgId},
  ///     sort: '-_meta.created_at', limit: 50,
  ///   ),
  /// ]);
  /// live.on('change', (state) => render(state.items));
  /// live.close();
  /// ```
  LiveView subscribe(
    List<SubscriptionSpec> specs, {
    Duration jitter = const Duration(milliseconds: 1000),
    Duration maxBackoff = const Duration(seconds: 30),
    double Function()? random,
  }) {
    if (specs.isEmpty) {
      throw ArgumentError.value(
          specs, 'specs', 'at least one subscription is required');
    }
    final view = LiveView._(this,
        jitter: jitter, maxBackoff: maxBackoff, random: random ?? _random.nextDouble);
    final names = <String>{};
    for (final (i, spec) in specs.indexed) {
      if (spec.db.isEmpty || spec.collection.isEmpty) {
        throw ArgumentError.value(
            spec, 'specs', 'each subscription needs `db` and `collection`');
      }
      final name = spec.id ?? '$i';
      if (!names.add(name)) {
        throw ArgumentError.value(
            name, 'specs', 'duplicate subscription id "$name"');
      }
      final sub = _Sub('s${++_seq}', name, spec, view);
      view.subs.add(sub);
      _subs[sub.wire] = sub;
    }
    _views.add(view);
    _schedule();
    return view;
  }

  /// The number of open views.
  int get open => _views.length;

  void _remove(LiveView view) {
    _views.remove(view);
    for (final s in view.subs) {
      _subs.remove(s.wire);
    }
    _schedule();
  }

  /// Reconciles the stream with the subscriptions, once per burst of
  /// changes.
  void _schedule() {
    if (_syncing) return;
    _syncing = true;
    scheduleMicrotask(() {
      _syncing = false;
      _reconcile();
    });
  }

  void _reconcile() {
    _gen++;
    _reader?.cancel();
    _reader = null;
    _ready = false;
    if (_subs.isEmpty) {
      return;
    }
    for (final v in _views) {
      v._status();
    }
    final gen = _gen;
    unawaited(_connect(gen).catchError((Object e, StackTrace st) {
      Zone.current.handleUncaughtError(e, st);
    }));
  }

  Future<void> _connect(int gen) async {
    var attempt = 0;
    while (gen == _gen) {
      var reconnectNow = false;
      try {
        final subscriptions = [
          for (final s in _subs.values)
            {
              'id': s.wire,
              'db': s.spec.db,
              'collection': s.spec.collection,
              if (s.spec.doc != null) 'doc': s.spec.doc,
              if (s.spec.doc == null && s.spec.where != null)
                'where': s.spec.where,
            },
        ];
        final res = await client.openStream(
          path: const ['_realtime'],
          body: {
            'subscriptions': subscriptions,
            if (_token != null) 'resume_from': _token,
          },
        );
        attempt = 0;
        reconnectNow = await _read(res, gen);
      } catch (err) {
        if (gen != _gen) return;
        if (err is InvalidResumeTokenError) {
          // The token is not good (another credential, a rotated key):
          // start over from a reload.
          _token = null;
          for (final s in _subs.values) {
            s.needsLoad = true;
          }
          continue;
        }
        if (err is RealtimeNotEnabledError ||
            err is ForbiddenError ||
            err is ValidationError ||
            err is AuthenticationError) {
          // A refusal that names a subscription ends the view that asked
          // for it; the others go on.
          final details = err is BackdException ? err.details : null;
          final at = details != null && details.isNotEmpty
              ? RegExp(r'^subscriptions\[(\d+)\]')
                  .firstMatch(details.first.path)
              : null;
          final bad = at != null
              ? _subs.values.elementAtOrNull(int.parse(at.group(1)!))
              : null;
          if (bad != null && _views.length > 1) {
            bad.view._fail(err);
            continue;
          }
          _fail(err);
          return;
        }
        if (err is! NetworkError && err is! BackdException) rethrow;
      }
      if (gen != _gen) return;
      _ready = false;
      for (final v in _views) {
        v._status();
      }
      if (reconnectNow) continue;
      final base = _reconnectBase.inMilliseconds * (1 << attempt++);
      final maxMs = _maxBackoff();
      final wait = base < maxMs ? base : maxMs;
      await Future<void>.delayed(Duration(milliseconds: wait));
    }
  }

  int _maxBackoff() {
    var max = 30000;
    for (final v in _views) {
      final ms = v.maxBackoff.inMilliseconds;
      if (ms < max) max = ms;
    }
    return max;
  }

  void _fail(Object err) {
    _gen++;
    _ready = false;
    for (final v in _views.toList()) {
      v._fail(err);
    }
  }

  /// Reads the stream until it ends. True when the next stream should open
  /// at once (the server asked: `reauth`, or a reset that needs the
  /// subscription made again).
  Future<bool> _read(TransportStreamResponse res, int gen) async {
    final buf = StringBuffer();
    final done = Completer<bool>();
    late StreamSubscription<List<int>> sub;
    var stop = false;
    sub = res.body.listen(
      (chunk) {
        buf.write(
            utf8.decode(chunk, allowMalformed: true).replaceAll('\r\n', '\n'));
        var text = buf.toString();
        var i = text.indexOf('\n\n');
        while (i >= 0 && !stop) {
          final block = text.substring(0, i);
          text = text.substring(i + 2);
          final ev = _parseBlock(block);
          if (ev != null) {
            if (gen != _gen) {
              stop = true;
              break;
            }
            final again = _handle(ev);
            if (again != null) {
              stop = true;
              if (!done.isCompleted) done.complete(again);
              break;
            }
          }
          i = text.indexOf('\n\n');
        }
        buf.clear();
        buf.write(text);
        if (gen != _gen && !done.isCompleted) {
          stop = true;
          done.complete(false);
        }
      },
      onDone: () {
        if (!done.isCompleted) done.complete(false);
      },
      onError: (Object e) {
        if (!done.isCompleted) done.completeError(e);
      },
    );
    _reader = sub;
    try {
      return await done.future;
    } finally {
      if (_reader == sub) _reader = null;
      await sub.cancel();
    }
  }

  /// What `_read` returns, when the stream is over; null while it goes on.
  bool? _handle(_Event ev) {
    final id = ev.id;
    if (id != null) _token = id;
    switch (ev.event) {
      case 'ready':
        _ready = true;
        for (final s in _subs.values) {
          if (s.needsLoad || !s.loaded) s.view._load(s, jitter: false);
        }
        for (final v in _views) {
          v._status();
        }
        return null;
      case 'created':
      case 'updated':
        final s = _subs[ev.data['sub']];
        if (s != null && ev.data['doc'] is Map) {
          s.view._apply(s,
              _DocEv(doc: Map<String, Object?>.from(ev.data['doc'] as Map)));
        }
        return null;
      case 'removed':
        final s = _subs[ev.data['sub']];
        if (s != null) {
          s.view._apply(
              s,
              _RemovedEv(
                  id: '${ev.data['id']}',
                  version: (ev.data['version'] as num?)?.toInt() ?? 0));
        }
        return null;
      case 'reset':
        var again = false;
        for (final sid in ev.data['subs'] as List? ?? const <Object?>[]) {
          final s = _subs[sid];
          if (s == null) continue;
          s.view.emit('reset', {
            'subscription': s.name,
            'reason': ev.data['reason'],
          });
          if (ev.data['reason'] == 'config_changed') {
            // The server dropped it: it has to be made again, on a new
            // stream, and loaded after it.
            s.needsLoad = true;
            again = true;
          } else {
            s.view._load(s, jitter: true);
          }
        }
        return again ? true : null;
      case 'session_ended':
        _fail(SessionEndedError(
            status: 401,
            code: 'session_ended',
            message:
                'the session, user or API key behind the stream is gone: '
                'sign in again'));
        return false;
      case 'reauth':
        // The credential's lifetime is over: reconnect, which sends the
        // current one.
        return true;
    }
    return null;
  }
}

final class _Event {
  _Event({this.id, required this.event, required this.data});
  final String? id;
  final String event;
  final Map<String, Object?> data;
}

_Event? _parseBlock(String block) {
  String? id;
  var event = '';
  final data = <String>[];
  for (final line in block.split('\n')) {
    if (line.startsWith(':')) continue;
    if (line.startsWith('id:')) {
      id = line.substring(3).trim();
    } else if (line.startsWith('event:')) {
      event = line.substring(6).trim();
    } else if (line.startsWith('data:')) {
      data.add(line.substring(5).trimLeft());
    }
  }
  if (event.isEmpty) return null;
  var parsed = <String, Object?>{};
  if (data.isNotEmpty) {
    try {
      final v = jsonDecode(data.join('\n'));
      if (v is Map) parsed = Map<String, Object?>.from(v);
    } catch (_) {
      return null;
    }
  }
  return _Event(id: id, event: event, data: parsed);
}

sealed class _Ev {
  const _Ev();
}

final class _DocEv extends _Ev {
  const _DocEv({required this.doc});
  final Map<String, Object?> doc;
}

final class _RemovedEv extends _Ev {
  const _RemovedEv({required this.id, required this.version});
  final String id;
  final int version;
}

final class _Sub {
  _Sub(this.wire, this.name, this.spec, this.view);

  /// The id the stream knows it by.
  final String wire;

  /// The id the app knows it by.
  final String name;
  final SubscriptionSpec spec;
  final LiveView view;
  var items = <String, Map<String, Object?>>{};
  var loaded = false;
  var needsLoad = true;
  var buffer = <_Ev>[];
  var generation = 0;
  var everLoaded = false;
}

/// A live view of one or more subscriptions.
final class LiveView {
  LiveView._(this._rt,
      {required this.jitter, required this.maxBackoff, required this.random});

  final Realtime _rt;
  final Duration jitter;
  final Duration maxBackoff;
  final double Function() random;

  final subs = <_Sub>[];
  final _handlers = <String, Set<void Function(Object?)>>{};
  var _closed = false;
  Object? _error;
  LiveStatus _lastStatus = LiveStatus.connecting;
  var _emitPending = false;
  final _refs = <String, Map<String, Map<String, Object?>>>{};
  final _refsAsked = <String>{};
  var _refsPending = false;
  Map<String, ({ReferenceSpec ref, List<String> ids})>? _pendingRefs;

  /// Whether the view was stopped, by `close()` or by an error.
  bool get closed => _closed;

  /// The error that stopped the view, if one did.
  Object? get error => _error;

  /// Listens: `change` (with the new [LiveState]), `status` (a
  /// [LiveStatus]), `reset` (`{subscription, reason}`, when the server
  /// asked for a reload), `access_lost` (`{subscription}`: a reload found
  /// the caller may no longer read it, and the list is empty) and `error`
  /// (an error that stopped the view). Returns a function that stops
  /// listening.
  void Function() on(String event, void Function(Object? arg) fn) {
    (_handlers[event] ??= {}).add(fn);
    return () => _handlers[event]?.remove(fn);
  }

  void emit(String event, Object? arg) {
    // Copy: a listener may stop listening while the event is being sent.
    for (final fn in {...?_handlers[event]}) {
      try {
        fn(arg);
      } catch (e) {
        Zone.current.handleUncaughtError(e, StackTrace.current);
      }
    }
  }

  /// The state now.
  LiveState get state => _snapshot();

  /// The first subscription's documents, in order.
  List<Map<String, Object?>> get items => _snapshot().items;

  /// Resolves with the state once every subscription has loaded; completes
  /// with the error if the view stops first.
  Future<LiveState> get ready {
    final completer = Completer<LiveState>();
    final err = _error;
    if (err != null) return Future.error(err);
    if (_computeStatus() == LiveStatus.live) {
      return Future.value(_snapshot());
    }
    void Function()? off;
    void Function()? offErr;
    off = on('status', (s) {
      if (s == LiveStatus.live) {
        off?.call();
        offErr?.call();
        completer.complete(_snapshot());
      }
    });
    offErr = on('error', (e) {
      off?.call();
      offErr?.call();
      completer.completeError(e ?? StateError('live view failed'));
    });
    return completer.future;
  }

  /// Stops following. Safe to call more than once.
  void close() {
    if (_closed) return;
    _closed = true;
    _rt._remove(this);
    _setStatus(LiveStatus.closed);
  }

  void _fail(Object err) {
    if (_closed) return;
    _error = err;
    _closed = true;
    _rt._views.remove(this);
    for (final s in subs) {
      _rt._subs.remove(s.wire);
    }
    _setStatus(LiveStatus.closed);
    emit('error', err);
  }

  LiveStatus _computeStatus() {
    if (_closed) return LiveStatus.closed;
    final everLive = subs.every((s) => s.everLoaded);
    final allLoaded = subs.every((s) => s.loaded);
    if (allLoaded && _rt._ready) return LiveStatus.live;
    return everLive || subs.any((s) => s.everLoaded)
        ? LiveStatus.resyncing
        : LiveStatus.connecting;
  }

  void _status() => _setStatus(_computeStatus());

  void _setStatus(LiveStatus s) {
    if (s == _lastStatus) return;
    _lastStatus = s;
    emit('status', s);
    _scheduleChange();
  }

  void _scheduleChange() {
    if (_emitPending) return;
    _emitPending = true;
    scheduleMicrotask(() {
      _emitPending = false;
      if ((_handlers['change'] ?? const {}).isNotEmpty) {
        emit('change', _snapshot());
      }
    });
  }

  LiveState _snapshot() {
    final subscriptions = <String, LiveSubscriptionState>{};
    for (final s in subs) {
      subscriptions[s.name] = LiveSubscriptionState(
        items: _ordered(s),
        status: s.loaded
            ? (_rt._ready ? LiveStatus.live : LiveStatus.resyncing)
            : s.everLoaded
                ? LiveStatus.resyncing
                : LiveStatus.connecting,
      );
    }
    return LiveState(
      status: _lastStatus,
      items: subs.isEmpty ? const [] : subscriptions[subs.first.name]!.items,
      subscriptions: subscriptions,
      references: {
        for (final e in _refs.entries) e.key: Map.of(e.value),
      },
    );
  }

  /// Loads (or reloads) a subscription's list. Events that arrive meanwhile
  /// wait in its buffer and are applied after, ignoring any whose version
  /// is not newer. A reload after a reset waits a random time first.
  void _load(_Sub s, {required bool jitter}) {
    if (_closed) return;
    s.needsLoad = false;
    s.loaded = false;
    s.buffer = [];
    final generation = ++s.generation;
    _status();
    Future<void> run() async {
      if (_closed || generation != s.generation) return;
      try {
        final items = await _fetch(s);
        if (_closed || generation != s.generation) return;
        s.items = {for (final d in items) '${d['id']}': d};
        for (final ev in s.buffer) {
          _applyNow(s, ev);
        }
        s.buffer = [];
        s.loaded = true;
        s.everLoaded = true;
        _trim(s);
        _collectReferences(s);
        _status();
        _scheduleChange();
      } catch (err) {
        if (_closed || generation != s.generation) return;
        if (err is ForbiddenError && s.everLoaded) {
          // Access was lost since the list was read (a member removed from
          // a team): the list is empty for this caller now. The view stays,
          // and loads again if a later reset says so.
          s.items = {};
          s.buffer = [];
          s.loaded = true;
          emit('access_lost', {'subscription': s.name});
          _status();
          _scheduleChange();
          return;
        }
        if (err is NetworkError ||
            (err is BackdException && err.status >= 500)) {
          // The list could not be read now: try again, after a pause.
          Timer(const Duration(seconds: 1), () => _load(s, jitter: false));
          return;
        }
        _fail(err);
      }
    }

    if (jitter) {
      Timer(
          Duration(
              milliseconds: (random() * this.jitter.inMilliseconds).floor()),
          () => unawaited(run()));
    } else {
      unawaited(run());
    }
  }

  Future<List<Map<String, Object?>>> _fetch(_Sub s) async {
    final col = _rt.client.db(s.spec.db).collection(s.spec.collection);
    final doc = s.spec.doc;
    if (doc != null) {
      try {
        return [await col.get(doc)];
      } on BackdException catch (err) {
        if (err.status == 404) return const [];
        rethrow;
      }
    }
    final want = s.spec.limit ?? 100;
    final out = <Map<String, Object?>>[];
    String? after;
    while (out.length < want) {
      final page = await col.list(
        where: s.spec.where,
        orderBy: s.spec.sort,
        limit: want - out.length < 100 ? want - out.length : 100,
        after: after,
      );
      out.addAll(page.items);
      if (!page.hasMore || page.nextCursor == null) break;
      after = page.nextCursor;
    }
    return out;
  }

  void _apply(_Sub s, _Ev ev) {
    if (_closed) return;
    if (!s.loaded) {
      s.buffer.add(ev);
      return;
    }
    _applyNow(s, ev);
    _trim(s);
    if (ev is _DocEv) _collectReferences(s);
    _scheduleChange();
  }

  void _applyNow(_Sub s, _Ev ev) {
    if (ev is _RemovedEv) {
      final held = s.items[ev.id];
      if (held != null && _versionOf(held) <= ev.version) {
        s.items.remove(ev.id);
      }
      return;
    }
    final doc = (ev as _DocEv).doc;
    final id = '${doc['id']}';
    final held = s.items[id];
    if (held != null && _versionOf(held) >= _versionOf(doc)) return;
    s.items[id] = doc;
  }

  /// Keeps a list within its `limit`, dropping what sorts last.
  void _trim(_Sub s) {
    final limit = s.spec.limit ?? 100;
    if (s.spec.doc != null || s.items.length <= limit) return;
    final keep = _ordered(s).sublist(0, limit);
    s.items = {for (final d in keep) '${d['id']}': d};
  }

  /// Finds referenced ids the view doesn't hold and reads them in one
  /// batched read per reference.
  void _collectReferences(_Sub s) {
    final refs = s.spec.references;
    if (refs == null) return;
    for (final entry in refs.entries) {
      final name = entry.key;
      final ref = entry.value;
      final held = _refs[name] ??= {};
      for (final d in s.items.values) {
        for (final id in _idsAt(d, ref.field)) {
          final key = '$name\u{0}$id';
          if (!held.containsKey(id) && !_refsAsked.contains(key)) {
            _refsAsked.add(key);
            final pending = _pendingRefs ??= {};
            pending.update(
                name,
                (v) => (ref: v.ref, ids: v.ids..add(id)),
                ifAbsent: () => (ref: ref, ids: [id]));
          }
        }
      }
    }
    if ((_pendingRefs?.isNotEmpty ?? false) && !_refsPending) {
      _refsPending = true;
      scheduleMicrotask(() => unawaited(_fetchReferences(s)));
    }
  }

  Future<void> _fetchReferences(_Sub s) async {
    _refsPending = false;
    final pending = _pendingRefs;
    _pendingRefs = null;
    if (pending == null || _closed) return;
    for (final entry in pending.entries) {
      final name = entry.key;
      final ref = entry.value.ref;
      final ids = entry.value.ids;
      final col =
          _rt.client.db(ref.db ?? s.spec.db).collection(ref.collection);
      final held = _refs[name] ??= {};
      for (var i = 0; i < ids.length; i += 100) {
        final chunk = ids.sublist(i, i + 100 > ids.length ? ids.length : i + 100);
        try {
          final page = await col.list(
              where: {
                'id': {'\$in': chunk},
              },
              limit: chunk.length);
          for (final d in page.items) {
            held['${d['id']}'] = _trimFields(d, ref.fields);
          }
        } catch (_) {
          // What can't be read stays unknown; the next change asks again.
          for (final id in chunk) {
            _refsAsked.remove('$name\u{0}$id');
          }
        }
      }
    }
    _scheduleChange();
  }
}

List<Map<String, Object?>> _ordered(_Sub s) {
  final items = s.items.values.toList();
  if (s.spec.doc != null) return items;
  final sort = s.spec.sort;
  final keys = <String>[
    if (sort is String) ...sort.split(','),
    if (sort is List) ...sort.map((e) => '$e'),
    if (sort == null) 'id',
  ].where((k) => k.isNotEmpty).toList();
  final parsed = [
    for (final k in keys)
      k.startsWith('-')
          ? (path: k.substring(1), dir: -1)
          : (path: k.startsWith('+') ? k.substring(1) : k, dir: 1),
  ];
  if (!parsed.any((p) => p.path == 'id')) {
    parsed.add((path: 'id', dir: 1));
  }
  items.sort((a, b) {
    for (final p in parsed) {
      final c = _compare(_at(a, p.path), _at(b, p.path));
      if (c != 0) return c * p.dir;
    }
    return 0;
  });
  return items;
}

int _compare(Object? a, Object? b) {
  if (a == b) return 0;
  if (a == null) return -1; // missing and null sort first, as in the database
  if (b == null) return 1;
  if (a is num && b is num) return a.compareTo(b);
  if (a is String && b is String) return a.compareTo(b);
  if (a is bool && b is bool) return a ? 1 : -1;
  return '$a'.compareTo('$b');
}

Object? _at(Object? doc, String path) {
  var v = doc;
  for (final p in path.split('.')) {
    if (v is! Map) return null;
    v = v[p];
  }
  return v;
}

List<String> _idsAt(Map<String, Object?> doc, String field) {
  final v = _at(doc, field);
  if (v is String) return [v];
  if (v is List) return [for (final x in v) if (x is String) x];
  return const [];
}

int _versionOf(Map<String, Object?> doc) {
  final meta = doc['_meta'];
  if (meta is Map) return (meta['version'] as num?)?.toInt() ?? 0;
  return 0;
}

Map<String, Object?> _trimFields(
    Map<String, Object?> doc, List<String>? fields) {
  if (fields == null) return doc;
  final out = <String, Object?>{'id': doc['id'], '_meta': doc['_meta']};
  for (final f in fields) {
    final v = _at(doc, f);
    if (v != null) out[f] = v;
  }
  return out;
}
