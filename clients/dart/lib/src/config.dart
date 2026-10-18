import 'dart:async';
import 'dart:convert';

import 'package:crypto/crypto.dart';

import 'client.dart';

/// Where `BackdClient.config` keeps its copies between runs: a file for
/// standalone apps, a SharedPreferences-backed store for Flutter (supplied
/// by the app so `backd` stays pure Dart). Memory only when not given.
abstract interface class ConfigStorage {
  /// The value for [key], or null.
  FutureOr<String?> getItem(String key);

  /// Stores [value] under [key].
  FutureOr<void> setItem(String key, String value);

  /// Forgets [key].
  FutureOr<void> removeItem(String key);
}

/// The default [ConfigStorage]: memory only.
final class MemoryConfigStorage implements ConfigStorage {
  final Map<String, String> _items = {};

  @override
  Future<String?> getItem(String key) async => _items[key];

  @override
  Future<void> setItem(String key, String value) async => _items[key] = value;

  @override
  Future<void> removeItem(String key) async => _items.remove(key);
}

/// One cached answer: the values of the paths asked for, with what
/// revalidates them.
final class ConfigEntry {
  ConfigEntry({
    required this.etag,
    required this.values,
    required this.version,
    required this.fetchedAt,
    required this.ttl,
  });

  factory ConfigEntry.fromJson(Map<String, Object?> json) => ConfigEntry(
        etag: json['etag'] as String? ?? '',
        values: json['values'] is Map
            ? Map<String, Object?>.from(json['values'] as Map)
            : const {},
        version: '${json['version'] ?? ''}',
        fetchedAt: json['fetchedAt'] as int? ?? 0,
        ttl: (json['ttl'] as num?)?.toInt() ?? 0,
      );

  Map<String, Object?> toJson() => {
        'etag': etag,
        'values': values,
        'version': version,
        'fetchedAt': fetchedAt,
        'ttl': ttl,
      };

  String etag;
  Map<String, Object?> values;
  String version;

  /// Milliseconds since the epoch.
  int fetchedAt;

  /// Seconds the copy is fresh for.
  int ttl;
}

const _prefix = 'backd.config:';

bool _isObject(Object? v) => v is Map;

/// The leaves of a nested document by dot path.
Map<String, Object?> flattenConfig(Map<String, Object?> values,
    [String prefix = '', Map<String, Object?>? out]) {
  out ??= {};
  for (final e in values.entries) {
    final p = prefix.isEmpty ? e.key : '$prefix.${e.key}';
    if (_isObject(e.value)) {
      flattenConfig(Map<String, Object?>.from(e.value as Map), p, out);
    } else {
      out[p] = e.value;
    }
  }
  return out;
}

Object? _at(Map<String, Object?> values, String path) {
  Object? cur = values;
  for (final k in path.split('.')) {
    if (cur is! Map || !cur.containsKey(k)) return null;
    cur = cur[k];
  }
  return cur;
}

int _now() => DateTime.now().millisecondsSinceEpoch;

/// The realm's app configuration: `client.config`. Values come from
/// `GET /v1/{realm}/_config`, which answers only what the signed-in user's
/// rules allow, with defaults filled in.
///
/// Copies are kept per realm and per user (in the client's `configStorage`,
/// memory by default): fresh for the `cache_ttl` the server gave, with no
/// request; after that reads return the copy at once and revalidate it in
/// the background with `If-None-Match`, which usually costs a `304`. They
/// are dropped on sign-out and when another user signs in.
///
/// ```dart
/// await client.config.load(['features', 'maintenance']);
/// client.config.get('maintenance.enabled'); // from the copy; fetched first if never loaded
/// client.config.changes.listen(render);     // the dot paths that changed
/// ```
final class Config {
  Config(this.client, [ConfigStorage? storage]) : _store = storage;

  final BackdClient<Object?> client;
  final ConfigStorage? _store;

  /// Entries by the paths they were asked with ('' is everything).
  final Map<String, ConfigEntry> _entries = {};

  /// The caller the entries belong to.
  String _owner = '';

  /// Owners seen this run, for [clear] to forget their persisted copies.
  final Set<String> _owners = {};

  final Map<String, Future<ConfigEntry>> _inflight = {};

  final _changes = StreamController<List<String>>.broadcast();

  /// The dot paths whose value changed when a read brought a different
  /// configuration.
  Stream<List<String>> get changes => _changes.stream;

  /// Who the copies belong to: the credentials of the client, hashed
  /// (nothing secret is stored). Resets the copies when it changes.
  Future<String> _scope() async {
    String? credential;
    if (client.apiKey != null) {
      credential = 'key:${client.apiKey}';
    } else {
      final token = await client.storage.get();
      if (token != null) credential = 'session:$token';
    }
    final owner = credential != null
        ? sha256.convert(utf8.encode(credential)).toString().substring(0, 16)
        : 'anonymous';
    if (owner != _owner) {
      _entries.clear();
      _owner = owner;
      _owners.add(owner);
      await _restore();
    }
    return owner;
  }

  String _storageKey() => '$_prefix${client.realm}:$_owner';

  Future<void> _restore() async {
    final store = _store;
    if (store == null) return;
    try {
      final raw = await store.getItem(_storageKey());
      if (raw == null) return;
      final saved = jsonDecode(raw);
      if (saved is! Map) return;
      for (final e in saved.entries) {
        final v = e.value;
        if (v is Map && v['etag'] is String && _isObject(v['values'])) {
          _entries['${e.key}'] =
              ConfigEntry.fromJson(Map<String, Object?>.from(v));
        }
      }
    } on FormatException {
      // Corrupt storage: start from nothing.
    }
  }

  Future<void> _persist() async {
    final store = _store;
    if (store == null) return;
    try {
      await store.setItem(
        _storageKey(),
        jsonEncode({for (final e in _entries.entries) e.key: e.value.toJson()}),
      );
    } catch (_) {
      // A full quota: the memory copy still works.
    }
  }

  /// Forgets every copy of this realm, for every user: on sign-out and when
  /// another user signs in. The client calls it; apps don't.
  Future<void> clear() async {
    _entries.clear();
    _inflight.clear();
    final store = _store;
    if (store != null) {
      for (final owner in _owners) {
        await store.removeItem('$_prefix${client.realm}:$owner');
      }
    }
    _owners.clear();
    _owner = '';
  }

  /// Fetches the configuration (or the groups and keys named) and keeps it.
  /// Without a request while the copy is fresh; with `refresh: true` always
  /// asks the server.
  ///
  /// [paths] are groups or keys in dot notation; everything readable by
  /// default. Returns the values, nested.
  Future<Map<String, Object?>> load(
    Object? paths, {
    bool refresh = false,
    RequestOptions? options,
  }) async {
    final key = _normalize(paths);
    await _scope();
    final entry = _entries[key];
    if (entry != null && !refresh && _fresh(entry)) return _view(key);
    await _fetch(key, options);
    return _view(key);
  }

  /// One value. From the copy held — a stale copy is revalidated in the
  /// background; a key whose group was never loaded is fetched first. A key
  /// the user can't read, or that doesn't exist, is null. `refresh: true`
  /// always asks the server.
  ///
  /// ```dart
  /// final enabled = await client.config.get('features.export.enabled');
  /// ```
  Future<Object?> get(
    String path, {
    bool refresh = false,
    RequestOptions? options,
  }) async {
    // The credentials may have changed since the last read; a scope change
    // clears the copies so nothing of another user is served.
    await _scope();
    final held = _cover(path);
    if (held != null && !refresh) {
      if (!_fresh(held.entry)) unawaited(_revalidate(held.key));
      return _at(held.entry.values, path);
    }
    final known = _cover(path);
    if (known != null && !refresh) {
      return _at(known.entry.values, path); // loaded while the scope was read
    }
    // A refresh asks again for the copy that holds the key; a key never
    // loaded, for its group.
    await _fetch(known?.key ?? path.split('.').first, options);
    final cover = _cover(path);
    return cover == null ? null : _at(cover.entry.values, path);
  }

  /// One value from the copy held, with no request at all: [fallback] when
  /// it was never loaded.
  Object? getSync(String path, [Object? fallback]) {
    final held = _cover(path);
    if (held == null) return fallback;
    return _at(held.entry.values, path) ?? fallback;
  }

  bool _fresh(ConfigEntry e) => _now() < e.fetchedAt + e.ttl * 1000;

  /// The entry that holds a path: the whole configuration, or a group that
  /// contains it.
  ({String key, ConfigEntry entry})? _cover(String path) {
    final all = _entries[''];
    if (all != null) return (key: '', entry: all);
    for (final e in _entries.entries) {
      for (final p in e.key.split(',')) {
        if (p.isNotEmpty && (path == p || path.startsWith('$p.'))) {
          return (key: e.key, entry: e.value);
        }
      }
    }
    return null;
  }

  Map<String, Object?> _view(String key) {
    final e = _entries[key];
    return e == null
        ? {}
        : Map<String, Object?>.from(
            jsonDecode(jsonEncode(e.values)) as Map);
  }

  /// Asks the server (with the tag of the copy held) and keeps the answer.
  /// Concurrent requests for the same key share one request.
  Future<ConfigEntry> _fetch(String key, RequestOptions? options) {
    final running = _inflight[key];
    if (running != null) return running;
    final p = _doFetch(key, options).whenComplete(() {
      // Block body: remove() returns this very future, and returning it
      // would make p wait on itself.
      _inflight.remove(key);
    });
    _inflight[key] = p;
    return p;
  }

  Future<ConfigEntry> _doFetch(String key, RequestOptions? options) async {
    final owner = _owner;
    final held = _entries[key];
    final res = await client.request(RequestInit(
      method: 'GET',
      path: const ['_config'],
      query: key.isEmpty ? null : {'paths': key},
      headers: {
        ...?options?.headers,
        if (held != null) 'If-None-Match': held.etag,
      },
      retry: options?.retry,
      cache: false,
    ));
    if (owner != _owner) {
      // The user changed while the request was out: what came back is not
      // theirs to keep.
      return held ??
          ConfigEntry(
              etag: '', values: const {}, version: '', fetchedAt: 0, ttl: 0);
    }
    if (res.status == 304 && held != null) {
      held.fetchedAt = _now();
      held.ttl = _ttlOf(res.headers, held.ttl);
      await _persist();
      return held;
    }
    final body = res.data is Map
        ? Map<String, Object?>.from(res.data as Map)
        : <String, Object?>{};
    final entry = ConfigEntry(
      etag: res.headers['etag']?.first ?? '',
      values: body['values'] is Map
          ? Map<String, Object?>.from(body['values'] as Map)
          : {},
      version: '${body['version'] ?? ''}',
      fetchedAt: _now(),
      ttl: body['cache_ttl'] is num ? (body['cache_ttl'] as num).toInt() : 0,
    );
    final before = flattenConfig(held?.values ?? const {});
    _entries[key] = entry;
    await _persist();
    if (held != null) {
      final after = flattenConfig(entry.values);
      final changed = {...before.keys, ...after.keys}
          .where((p) => jsonEncode(before[p]) != jsonEncode(after[p]))
          .toList()
        ..sort();
      if (changed.isNotEmpty) _changes.add(changed);
    }
    return entry;
  }

  /// Background refresh of a stale copy: failures keep serving the copy held.
  Future<void> _revalidate(String key) async {
    try {
      await _fetch(key, null);
    } catch (_) {
      // Offline or refused: the copy held keeps serving, and the next read
      // tries again.
    }
  }
}

/// The key of a request: its paths, trimmed, sorted and without repeats;
/// '' for everything.
String _normalize(Object? paths) {
  final list = (paths is List ? paths.map((p) => '$p') : '${paths ?? ''}'.split(','))
      .map((p) => p.trim())
      .where((p) => p.isNotEmpty)
      .toSet()
      .toList()
    ..sort();
  return list.join(',');
}

/// The `max-age` a `304` repeats, if any, else the ttl held.
int _ttlOf(Map<String, List<String>> headers, int fallback) {
  final m = RegExp('max-age=(\\d+)', caseSensitive: false)
      .firstMatch(headers['cache-control']?.first ?? '');
  return m != null ? int.parse(m.group(1)!) : fallback;
}
