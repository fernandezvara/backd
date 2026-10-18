import 'dart:convert';
import 'dart:io';
import 'dart:typed_data' show BytesBuilder;

/// How much of an upload has been sent.
final class UploadProgress {
  const UploadProgress({required this.loaded, required this.total});

  /// Bytes sent.
  final int loaded;

  /// Bytes in all, when known (0 otherwise).
  final int total;
}

/// One request as the transport sees it. The [body] is already encoded;
/// JSON bodies arrive as UTF-8 bytes.
final class TransportRequest {
  const TransportRequest({
    required this.method,
    required this.url,
    this.headers = const {},
    this.body,
  });

  final String method;
  final Uri url;
  final Map<String, String> headers;
  final List<int>? body;
}

/// The answer, fully read. Header names are lower-cased for lookup.
final class TransportResponse {
  TransportResponse({
    required this.status,
    this.reasonPhrase,
    Map<String, List<String>>? headers,
    this.body = const [],
  }) : headers = headers ?? const {};

  final int status;
  final String? reasonPhrase;

  /// Response headers by lower-cased name.
  final Map<String, List<String>> headers;

  /// The response body.
  final List<int> body;

  /// The first value of a header, whatever its case; null when absent.
  String? header(String name) {
    final values = headers[name.toLowerCase()];
    return (values == null || values.isEmpty) ? null : values.first;
  }

  /// The body decoded as UTF-8.
  String get bodyText => utf8.decode(body, allowMalformed: true);
}

/// Sends one request and answers with the full response. This is the seam
/// tests implement to stand in for the network — the `fetch` parameter of
/// the JavaScript client.
///
/// The default implementation is [HttpTransport], over `dart:io`'s
/// `HttpClient`. Requests go out as given: the transport adds no headers of
/// its own.
typedef Transport = Future<TransportResponse> Function(TransportRequest request);

/// The default [Transport]: `dart:io`'s `HttpClient`. The client that builds
/// one itself closes it in `BackdClient.close`; a transport passed in stays
/// the caller's to close.
final class HttpTransport {
  /// Wraps [client], or a new `HttpClient` when null.
  HttpTransport([HttpClient? client]) : _client = client ?? HttpClient();

  final HttpClient _client;

  /// Sends the request and reads the whole answer.
  Future<TransportResponse> call(TransportRequest req) async {
    final request = await _client.openUrl(req.method, req.url);
    req.headers.forEach(request.headers.set);
    if (req.body != null) request.add(req.body!);
    final response = await request.close();

    final headers = <String, List<String>>{};
    response.headers.forEach((name, values) {
      headers[name.toLowerCase()] = values;
    });
    final bytes = BytesBuilder(copy: false);
    await for (final chunk in response) {
      bytes.add(chunk);
    }
    return TransportResponse(
      status: response.statusCode,
      reasonPhrase: response.reasonPhrase,
      headers: headers,
      body: bytes.takeBytes(),
    );
  }

  /// Closes the client. Pending requests fail with [force].
  void close({bool force = false}) => _client.close(force: force);
}

/// Parses a `Retry-After` header (seconds or an HTTP date) into a delay.
Duration? parseRetryAfter(String? value) {
  if (value == null || value.isEmpty) return null;
  final seconds = int.tryParse(value);
  if (seconds != null) return Duration(seconds: seconds);
  try {
    final at = HttpDate.parse(value);
    final ms = at.difference(DateTime.now()).inMilliseconds;
    return ms < 0 ? Duration.zero : Duration(milliseconds: ms);
  } catch (_) {
    // HttpException, FormatException — not a date we understand.
    return null;
  }
}
