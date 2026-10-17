import 'package:backd/backd.dart';
import 'package:backd/src/cache.dart' show cacheable;
import 'package:test/test.dart';

CacheEntry entry(String tag, [String? body]) =>
    CacheEntry(etag: tag, text: body ?? '{"tag":"$tag"}', headers: const {});

void main() {
  group('cacheable', () {
    test('document and list GETs only', () {
      bool ok(List<String> path, {Map<String, Object?>? query}) => cacheable(
            method: 'GET',
            path: path,
            query: query,
          );
      expect(ok(['main', 'posts']), isTrue);
      expect(ok(['main', 'posts', 'abc']), isTrue);
      expect(ok(['main']), isFalse);
      expect(ok(['main', 'posts', 'a', 'too', 'deep']), isFalse);
      expect(
        cacheable(method: 'POST', path: ['main', 'posts']),
        isFalse,
      );
      expect(
        cacheable(method: 'GET', path: ['main', 'posts'], raw: true),
        isFalse,
      );
    });

    test('no _-paths', () {
      expect(
        cacheable(method: 'GET', path: ['main', '_config']),
        isFalse,
      );
      expect(
        cacheable(method: 'GET', path: ['_system', 'x', 'y']),
        isFalse,
      );
    });

    test('no file_links or expand reads', () {
      expect(
        cacheable(
          method: 'GET',
          path: ['main', 'posts'],
          query: {'file_links': 'true'},
        ),
        isFalse,
      );
      expect(
        cacheable(
          method: 'GET',
          path: ['main', 'posts'],
          query: {'expand': 'author'},
        ),
        isFalse,
      );
      expect(
        cacheable(
          method: 'GET',
          path: ['main', 'posts'],
          query: {'limit': 20},
        ),
        isTrue,
      );
    });
  });

  group('RevalidationCache', () {
    test('get marks entries most recently used', () {
      final cache = RevalidationCache(const CacheOptions(maxEntries: 2));
      cache.set('a', entry('1'));
      cache.set('b', entry('2'));
      cache.get('a'); // b is now oldest
      cache.set('c', entry('3'));
      expect(cache.entries.containsKey('a'), isTrue);
      expect(cache.entries.containsKey('b'), isFalse);
      expect(cache.entries.containsKey('c'), isTrue);
    });

    test('evicts by entry count and by bytes', () {
      final byCount = RevalidationCache(const CacheOptions(maxEntries: 2));
      byCount.set('a', entry('1'));
      byCount.set('b', entry('2'));
      byCount.set('c', entry('3'));
      expect(byCount.entries.keys.toList(), ['b', 'c']);

      final byBytes = RevalidationCache(const CacheOptions(maxBytes: 30));
      byBytes.set('a', entry('1', 'x' * 10));
      byBytes.set('b', entry('2', 'x' * 10));
      byBytes.set('c', entry('3', 'x' * 25));
      expect(byBytes.entries.containsKey('a'), isFalse);
      expect(byBytes.entries.containsKey('c'), isTrue);
      expect(byBytes.bytes, 25);
    });

    test('a body bigger than maxBytes is never kept', () {
      final cache = RevalidationCache(const CacheOptions(maxBytes: 5));
      cache.set('big', entry('1', 'x' * 10));
      expect(cache.entries.isEmpty, isTrue);
      expect(cache.bytes, 0);
    });

    test('scope empties the cache for another owner', () {
      final cache = RevalidationCache();
      cache.scope('session:a');
      cache.set('k', entry('1'));
      cache.scope('session:a');
      expect(cache.entries, hasLength(1));
      cache.scope('session:b');
      expect(cache.entries, isEmpty);
      expect(cache.bytes, 0);
    });

    test('dropPath clears every query of the path', () {
      final cache = RevalidationCache();
      cache.set('http://h/v1/r/main/posts/a', entry('1'));
      cache.set('http://h/v1/r/main/posts/a?x=1', entry('2'));
      cache.set('http://h/v1/r/main/posts/b', entry('3'));
      cache.dropPath('/v1/r/main/posts/a');
      expect(cache.entries.keys.single, 'http://h/v1/r/main/posts/b');
    });

    test('stats report hits, misses and size', () {
      final cache = RevalidationCache();
      cache
        ..hits = 2
        ..misses = 3
        ..set('k', entry('1', 'x' * 7));
      final stats = cache.stats();
      expect(stats.hits, 2);
      expect(stats.misses, 3);
      expect(stats.entries, 1);
      expect(stats.bytes, 7);
      cache.clear();
      expect(cache.stats().entries, 0);
    });
  });
}
