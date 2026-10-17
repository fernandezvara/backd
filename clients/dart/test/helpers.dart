import 'dart:async';
import 'dart:collection';
import 'dart:convert';

import 'package:backd/backd.dart';

/// Builds a [TransportResponse] with a JSON (or raw string) body.
TransportResponse respond(
  int status, [
  Object? body,
  Map<String, String>? headers,
]) {
  return TransportResponse(
    status: status,
    reasonPhrase: 'status $status',
    headers: {
      for (final e in (headers ?? const {}).entries) e.key.toLowerCase(): [
        e.value,
      ],
    },
    body: body == null
        ? const []
        : utf8.encode(body is String ? body : jsonEncode(body)),
  );
}

/// A [Transport] standing in for the network: it records every request and
/// answers (or throws) what was queued, in order.
final class FakeTransport {
  final List<TransportRequest> requests = [];
  final Queue<Object> _answers = Queue();

  /// Queues an answer.
  void answer(TransportResponse response) => _answers.add(response);

  /// Queues a failure (a socket exception, a timeout — anything a real
  /// transport may throw).
  void fail(Object error) => _answers.add(error);

  Future<TransportResponse> call(TransportRequest request) {
    requests.add(request);
    if (_answers.isEmpty) {
      return Future.error(StateError('FakeTransport: no queued answer'));
    }
    final next = _answers.removeFirst();
    if (next is TransportResponse) return Future.value(next);
    return Future.error(next);
  }
}

/// A [StreamTransport] for tests: each call takes the next queued
/// [Stream<List<int>>] (or fails it).
final class FakeStreamTransport {
  final List<TransportRequest> requests = [];
  final Queue<Object> _streams = Queue();

  /// Queues an SSE body (encoded text in one chunk).
  void stream(String body) => _streams.add(Stream.value(utf8.encode(body)));

  /// Queues a stream built from chunks.
  void chunks(List<List<int>> chunks) =>
      _streams.add(Stream.fromIterable(chunks));

  /// Queues a stream the test drives (or fails) itself.
  void controlled(Stream<List<int>> stream) => _streams.add(stream);

  /// Queues a failure opening the stream.
  void fail(Object error) => _streams.add(error);

  Future<TransportStreamResponse> call(TransportRequest request) {
    requests.add(request);
    if (_streams.isEmpty) {
      return Future.error(
          StateError('FakeStreamTransport: no queued stream'));
    }
    final next = _streams.removeFirst();
    if (next is Stream<List<int>>) {
      return Future.value(TransportStreamResponse(
        status: 200,
        headers: const {},
        body: next,
      ));
    }
    return Future.error(next);
  }
}

/// Finds a request's header whatever its case.
String? headerOf(TransportRequest request, String name) {
  for (final e in request.headers.entries) {
    if (e.key.toLowerCase() == name.toLowerCase()) return e.value;
  }
  return null;
}

/// A client over [transport], with no retry waits.
BackdClient<Object?> testClient(
  FakeTransport transport, {
  String? apiKey,
  TokenStorage? storage,
  ConfigStorage? configStorage,
  RetryOptions? retry,
  CacheOptions? cache,
  Map<String, String>? headers,
  String? types,
  void Function()? onStaleTypes,
  FakeStreamTransport? streamTransport,
}) {
  return BackdClient(
    url: 'http://backd.test',
    realm: 'shelf',
    apiKey: apiKey,
    storage: storage,
    configStorage: configStorage,
    retry: retry ?? const RetryOptions(),
    cache: cache,
    headers: headers ?? const {},
    transport: transport.call,
    streamTransport: streamTransport?.call,
    types: types,
    onStaleTypes: onStaleTypes,
    backoff: (attempt, retryAfter, maxDelay) => Duration.zero,
  );
}
