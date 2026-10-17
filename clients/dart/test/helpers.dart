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
    types: types,
    onStaleTypes: onStaleTypes,
    backoff: (attempt, retryAfter, maxDelay) => Duration.zero,
  );
}
