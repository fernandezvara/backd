import 'dart:async';
import 'dart:convert';
import 'dart:io' show HttpClient;

import 'package:meta/meta.dart';

import 'cache.dart';
import 'config.dart';
import 'errors.dart';
import 'storage.dart';
import 'transport.dart';

/// Retry settings. Only `429` and `503` answers are retried, and only for
/// safe requests: GET, or writes with `If-Match` or `Idempotency-Key`.
final class RetryOptions {
  const RetryOptions({this.attempts = 0, this.maxDelay = _defaultMaxDelay});

  static const _defaultMaxDelay = Duration(seconds: 30);

  /// Extra attempts after the first (0 disables).
  final int attempts;

  /// Longest wait between attempts.
  final Duration maxDelay;
}

/// The fresh proof of identity `client.auth.reauth()` gave, sent with the
/// sensitive requests while it is good.
final class ReauthProof {
  const ReauthProof({required this.token, required this.expires});

  final String token;
  final DateTime expires;
}

/// Per-request options, as the client's modules build them.
final class RequestInit {
  const RequestInit({
    required this.method,
    required this.path,
    this.query,
    this.body,
    this.rawBody,
    this.raw = false,
    this.contentType,
    this.headers,
    this.auth = true,
    this.expire = true,
    this.reauth = false,
    this.retry,
    this.cache = true,
    this.onUploadProgress,
  });

  /// HTTP method.
  final String method;

  /// Path segments after `/v1/{realm}`, encoded by the client.
  final List<String> path;

  /// Query parameters; null values are skipped, the rest stringified.
  final Map<String, Object?>? query;

  /// Sent as JSON.
  final Object? body;

  /// Sent as it is (a file's bytes), instead of [body].
  final List<int>? rawBody;

  /// Don't read the answer as JSON: it comes back as [RawResponse.response].
  final bool raw;

  /// Defaults to `application/json` when there's a [body]
  /// (`application/octet-stream` with [rawBody]).
  final String? contentType;

  /// Extra headers for this request, after the client's.
  final Map<String, String>? headers;

  /// Send credentials; default true.
  final bool auth;

  /// Treat a refused session token as expired; default true.
  final bool expire;

  /// A sensitive action: send the fresh proof `client.auth.reauth()` gave,
  /// while it is good.
  final bool reauth;

  /// Retries for this request, overriding the client's.
  final RetryOptions? retry;

  /// `false` skips the client's revalidation cache for this read.
  final bool cache;

  /// Called as the body is sent; without a streamed body, once when sent.
  final void Function(UploadProgress)? onUploadProgress;
}

/// The raw answer of a successful request.
final class RawResponse {
  const RawResponse({
    required this.status,
    required this.headers,
    this.data,
    this.response,
    this.fromCache = false,
  });

  final int status;
  final Map<String, List<String>> headers;

  /// Parsed JSON, or null for empty bodies.
  final Object? data;

  /// With [RequestInit.raw]: the answer, already read in [TransportResponse.body].
  final TransportResponse? response;

  /// The server answered `304` and [data] is the body kept from an earlier read.
  final bool fromCache;
}

/// A client for one realm of one backd server.
///
/// ```dart
/// final backd = BackdClient(url: 'http://localhost:8080', realm: 'shelf');
/// ```
///
/// Everything it knows how to do hangs off this type; when the realm has
/// generated types (`backd types --lang dart`), `BackdClient<Realm>` binds
/// them — without the parameter everything works on `Map<String, Object?>`.
class BackdClient<R> {
  /// Creates a client. [url] is the base URL of backd, e.g.
  /// `https://api.example.com`, and [realm] the realm to talk to.
  ///
  /// - [apiKey] is for server-side programs only: an API key (`bdk_…`) gives
  ///   full access to the realm.
  /// - [storage] is where the session token lives; memory by default.
  /// - [retry] enables retries for `429`/`503`; off by default.
  /// - [cache] keeps the last body and `ETag` of document and list reads in
  ///   memory and revalidates them with `If-None-Match`, so an unchanged read
  ///   costs a `304`. On by default; [CacheOptions.enabled] `false` turns it
  ///   off, and `maxEntries`/`maxBytes` set the bounds (200, 5 MB).
  /// - [configStorage] is where `client.config` keeps its copies between
  ///   runs; memory only when not given.
  /// - [transport] sends the requests: the `HttpClient`-backed transport by
  ///   default, a test double otherwise. A transport passed in stays the
  ///   caller's to close; [httpClient] configures the default one.
  /// - [headers] are sent with every request.
  /// - [types] is for development builds: the fingerprint of the generated
  ///   types the app was written against (`fingerprint` in `realm.json`, made
  ///   by `backd types`). It is sent as `X-Backd-Types`; when the server's
  ///   config has moved on it answers `X-Backd-Types-Stale` and the client
  ///   warns once. Leave it out in production: nothing is sent then.
  /// - [onStaleTypes] is called, once, when the server says the types are
  ///   stale; logs by default.
  BackdClient({
    required String url,
    required this.realm,
    this.apiKey,
    TokenStorage? storage,
    this.retry = const RetryOptions(),
    CacheOptions? cache,
    this.configStorage,
    this.headers = const {},
    Transport? transport,
    HttpClient? httpClient,
    this.types,
    this.onStaleTypes,
    @visibleForTesting
    Duration Function(int attempt, Duration? retryAfter, Duration maxDelay)
        backoff = _defaultBackoff,
  })  : url = url.replaceAll(RegExp('/+\$'), ''),
        storage = storage ?? MemoryTokenStorage(),
        cache = (cache ?? const CacheOptions()).enabled
            ? RevalidationCache(cache ?? const CacheOptions())
            : null,
        _backoff = backoff {
    if (this.url.isEmpty) {
      throw ArgumentError.value(url, 'url', 'is required');
    }
    if (realm.isEmpty) {
      throw ArgumentError.value(realm, 'realm', 'is required');
    }
    if (transport == null) {
      _ownedTransport = HttpTransport(httpClient);
      _transport = _ownedTransport!.call;
    } else {
      _transport = transport;
    }
  }

  /// Base URL of backd, without trailing slashes.
  final String url;

  /// The realm this client talks to.
  final String realm;

  /// Server-side only: an API key (`bdk_…`). Full access to the realm.
  final String? apiKey;

  /// Where the session token lives.
  final TokenStorage storage;

  /// Retries for `429`/`503` on safe requests.
  final RetryOptions retry;

  /// The revalidation cache; absent when created with cache disabled.
  final RevalidationCache? cache;

  /// Where `client.config` keeps its copies between runs; null for memory.
  final ConfigStorage? configStorage;

  /// Extra headers for every request.
  final Map<String, String> headers;

  /// The `X-Backd-Types` fingerprint sent in development, when set.
  final String? types;

  /// Called once when the server reports stale generated types.
  final void Function()? onStaleTypes;

  /// The fresh proof of identity `client.auth.reauth()` gave, sent with the
  /// sensitive requests while it is good. Set by the auth module (28.2).
  @internal
  ReauthProof? proof;

  /// Called by the request pipeline when a session token the server refused
  /// (`unauthenticated`) should expire: the auth module (28.2) clears storage
  /// and emits the `expired` event.
  @internal
  FutureOr<void> Function()? onSessionExpired;

  late final Transport _transport;
  HttpTransport? _ownedTransport;
  final Duration Function(int attempt, Duration? retryAfter, Duration maxDelay)
      _backoff;
  bool _staleReported = false;
  bool _closed = false;

  /// Whether the client was created with an API key.
  bool get hasApiKey => apiKey != null;

  /// How the revalidation cache is doing: reads the server answered `304` to
  /// ([CacheStats.hits]), cacheable reads that came back in full
  /// ([CacheStats.misses]), and what is kept.
  CacheStats cacheStats() =>
      cache?.stats() ??
      const CacheStats(hits: 0, misses: 0, entries: 0, bytes: 0);

  /// Forgets everything the revalidation cache holds.
  void clearCache() => cache?.clear();

  /// Releases the HTTP client owned by the default transport. Standalone
  /// programs should call it so the process can exit; a transport passed in
  /// stays the caller's to close.
  void close() {
    _closed = true;
    _ownedTransport?.close();
  }

  /// Sends a request to `/v1/{realm}/…`, adding credentials, and returns the
  /// parsed answer. Non-2xx answers throw a [BackdException].
  Future<RawResponse> request(RequestInit req) async {
    if (_closed) throw StateError('BackdClient is closed');
    var uri = Uri.parse(
      '$url/v1/${[realm, ...req.path].map(Uri.encodeComponent).join('/')}',
    );
    final query = <String, String>{...uri.queryParameters};
    req.query?.forEach((k, v) {
      if (v != null) query[k] = '$v';
    });
    uri = uri.replace(queryParameters: query);

    final sentHeaders = <String, String>{
      'Accept': req.raw ? '*/*' : 'application/json',
      ...headers,
      if (req.headers != null) ...req.headers!,
    };
    final List<int>? requestBody;
    if (req.rawBody != null) {
      requestBody = req.rawBody;
      sentHeaders['Content-Type'] =
          req.contentType ?? 'application/octet-stream';
    } else if (req.body != null) {
      requestBody = utf8.encode(jsonEncode(req.body));
      sentHeaders['Content-Type'] = req.contentType ?? 'application/json';
    } else {
      requestBody = null;
    }

    if (types != null) sentHeaders['X-Backd-Types'] = types!;
    if (req.reauth &&
        proof != null &&
        proof!.expires.isAfter(DateTime.now())) {
      sentHeaders['X-Backd-Reauth'] = proof!.token;
    }
    var sentSession = false;

    /// The signed-in caller as the cache knows it: entries read as another
    /// caller are never reused.
    var owner = '';
    if (req.auth) {
      if (apiKey != null) {
        sentHeaders['Authorization'] = 'Bearer $apiKey';
        owner = 'key:$apiKey';
      } else {
        final token = await storage.get();
        if (token != null) {
          sentHeaders['Authorization'] = 'Bearer $token';
          sentSession = true;
          owner = 'session:$token';
        }
      }
    }

    // Revalidation: send the tag of the copy we hold, if any.
    final effectiveCache = cache != null &&
            req.cache &&
            cacheable(
              method: req.method,
              path: req.path,
              query: req.query,
              raw: req.raw,
            )
        ? cache!
        : null;
    final key = uri.toString();
    CacheEntry? held;
    if (effectiveCache != null) {
      effectiveCache.scope(owner);
      // A caller's own If-None-Match is theirs to handle: the 304 goes back
      // to them.
      final theirs = sentHeaders.keys
          .any((h) => h.toLowerCase() == 'if-none-match');
      if (!theirs) {
        held = effectiveCache.get(key);
        if (held != null) sentHeaders['If-None-Match'] = held.etag;
      }
    }
    final writesDocument = req.method != 'GET' &&
        req.method != 'HEAD' &&
        req.path.length == 3 &&
        !req.path[0].startsWith('_') &&
        !req.path[1].startsWith('_');

    final effectiveRetry = req.retry ?? retry;
    // Safe to repeat: reads, writes conditional on a version, and requests
    // the server deduplicates by key.
    final headerNames = sentHeaders.keys.map((h) => h.toLowerCase());
    final safe = req.method == 'GET' ||
        headerNames.any((h) => h == 'if-match' || h == 'idempotency-key');
    final attempts = safe && effectiveRetry.attempts > 0
        ? effectiveRetry.attempts
        : 0;

    for (var attempt = 0; ; attempt++) {
      final TransportResponse res;
      try {
        res = await _transport(
          TransportRequest(
            method: req.method,
            url: uri,
            headers: sentHeaders,
            body: requestBody,
          ),
        );
      } catch (cause) {
        if (cause is BackdException) rethrow;
        throw NetworkError(
          status: 0,
          code: 'network_error',
          message: 'network error: $cause',
          cause: cause,
        );
      }
      req.onUploadProgress?.call(
        UploadProgress(
          loaded: requestBody?.length ?? 0,
          total: requestBody?.length ?? 0,
        ),
      );
      if (types != null &&
          !_staleReported &&
          res.header('x-backd-types-stale') == 'true') {
        _staleReported = true;
        if (onStaleTypes != null) {
          onStaleTypes!();
        } else {
          // ignore: avoid_print — one deliberate warning, like console.warn.
          print('backd: the generated types are out of date with the server: '
              'run `backd types` and commit the result');
        }
      }
      if (res.status == 304 && effectiveCache != null && held != null) {
        effectiveCache.hits++;
        // The copy we hold is current; its headers (ETag, …) are the ones
        // the 304 repeats.
        return RawResponse(
          status: 200,
          headers: held.headers,
          data: jsonDecode(held.text),
          fromCache: true,
        );
      }
      // The caller asked with their own If-None-Match: the 304 is their
      // answer.
      if (res.status == 304) {
        return RawResponse(status: 304, headers: res.headers);
      }
      final ok = res.status >= 200 && res.status < 300;
      if (ok && writesDocument) {
        // A write changed it: what we hold is stale.
        cache?.dropPath(uri.path);
      }
      if (ok && req.raw) {
        return RawResponse(
          status: res.status,
          headers: res.headers,
          response: res,
        );
      }
      if (ok) {
        final text = res.bodyText;
        if (effectiveCache != null) {
          effectiveCache.misses++;
          final etag = res.header('etag');
          final noStore =
              (res.header('cache-control') ?? '').contains(RegExp('no-store', caseSensitive: false));
          if (etag != null && !noStore && text.isNotEmpty) {
            effectiveCache.set(
              key,
              CacheEntry(etag: etag, text: text, headers: res.headers),
            );
          } else {
            effectiveCache.delete(key);
          }
        }
        return RawResponse(
          status: res.status,
          headers: res.headers,
          data: text.isEmpty ? null : jsonDecode(text),
        );
      }
      final err = errorFromResponse(res);
      if (err.code == 'unauthenticated' && sentSession && req.expire) {
        await onSessionExpired?.call();
      }
      if (err is RetryableError && attempt < attempts) {
        await Future<void>.delayed(
          _backoff(attempt, err.retryAfter, effectiveRetry.maxDelay),
        );
        continue;
      }
      throw err;
    }
  }
}

/// Wait before the next attempt: what the server asked, else exponential
/// backoff from 500 ms; never more than [max].
Duration _defaultBackoff(int attempt, Duration? retryAfter, Duration max) {
  final fallback = Duration(milliseconds: 500 << attempt);
  final delay = retryAfter ?? fallback;
  return delay > max ? max : delay;
}
