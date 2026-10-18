/// Revalidation cache: the last body and `ETag` of each document and list
/// read, kept in memory so the next read can send `If-None-Match` and take a
/// `304`. Nothing here is served without asking the server: every read still
/// goes out, and the server (which runs the rules) decides.
library;

/// Cache bounds for [RevalidationCache]; `enabled: false` is the `false` of
/// the JavaScript client's `cache` option.
final class CacheOptions {
  const CacheOptions({
    this.enabled = true,
    this.maxEntries = 200,
    this.maxBytes = 5 * 1024 * 1024,
  });

  /// `false` turns the cache off.
  final bool enabled;

  /// Most entries kept.
  final int maxEntries;

  /// Most bytes of bodies kept.
  final int maxBytes;
}

/// What `BackdClient.cacheStats` reports.
final class CacheStats {
  const CacheStats({
    required this.hits,
    required this.misses,
    required this.entries,
    required this.bytes,
  });

  /// Reads the server answered `304` to: the cached body was used.
  final int hits;

  /// Cacheable reads that came back in full.
  final int misses;

  /// Entries held.
  final int entries;

  /// Bytes of bodies held.
  final int bytes;
}

/// One cached read.
final class CacheEntry {
  const CacheEntry({
    required this.etag,
    required this.text,
    required this.headers,
  });

  final String etag;

  /// The body, as received.
  final String text;

  /// The response headers, to replay on a `304`.
  final Map<String, List<String>> headers;
}

/// In-memory, LRU-bounded store of [CacheEntry]. Entries belong to the
/// credentials of the request that read them — see [scope].
final class RevalidationCache {
  RevalidationCache([CacheOptions options = const CacheOptions()])
      : maxEntries = options.maxEntries,
        maxBytes = options.maxBytes;

  final int maxEntries;
  final int maxBytes;

  /// Insertion order is recency: the oldest is first.
  final Map<String, CacheEntry> entries = {};

  int bytes = 0;
  int hits = 0;
  int misses = 0;
  String _owner = '';

  /// Empties the cache when the caller isn't the one its entries were read
  /// as. [owner] identifies the credentials of the request; it is never sent
  /// anywhere.
  void scope(String owner) {
    if (owner != _owner) {
      clear();
      _owner = owner;
    }
  }

  /// The entry for [key]; marks it most recently used.
  CacheEntry? get(String key) {
    final entry = entries.remove(key);
    if (entry != null) entries[key] = entry;
    return entry;
  }

  /// Keeps [entry] for [key], evicting the oldest until the bounds hold. A
  /// body that alone would exceed [maxBytes] is not worth keeping.
  void set(String key, CacheEntry entry) {
    delete(key);
    if (entry.text.length > maxBytes) return;
    entries[key] = entry;
    bytes += entry.text.length;
    while (entries.length > maxEntries || bytes > maxBytes) {
      if (entries.isEmpty) break;
      delete(entries.keys.first);
    }
  }

  void delete(String key) {
    final entry = entries.remove(key);
    if (entry != null) bytes -= entry.text.length;
  }

  /// Drops every entry of a path, whatever its query: a document after a
  /// write to it.
  void dropPath(String pathname) {
    for (final key in entries.keys.toList()) {
      if (Uri.parse(key).path == pathname) delete(key);
    }
  }

  void clear() {
    entries.clear();
    bytes = 0;
  }

  CacheStats stats() => CacheStats(
        hits: hits,
        misses: misses,
        entries: entries.length,
        bytes: bytes,
      );
}

/// Whether a request is a document or list read the cache may keep:
/// `[database, collection]` or `[database, collection, id]` GET reads,
/// without the parameters that make an answer per-response (signed file
/// links, expansions).
bool cacheable({
  required String method,
  required List<String> path,
  Map<String, Object?>? query,
  bool raw = false,
}) {
  if (method != 'GET' || raw || path.length < 2 || path.length > 3) {
    return false;
  }
  if (path.any((s) => s.startsWith('_'))) return false;
  return query?['file_links'] == null && query?['expand'] == null;
}
