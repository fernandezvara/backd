import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'client.dart';

/// A ready export, to save: either a link signed for a few minutes ([url]) or,
/// in a realm whose storage clients cannot reach, the zip itself.
final class ExportDownload {
  ExportDownload._({this.url, this.expiresAt, required Future<Uint8List> Function() bytes})
      : _bytes = bytes;

  /// A link signed for a few minutes: open it, or read it with [bytes].
  final String? url;

  /// When the link stops working.
  final String? expiresAt;

  final Future<Uint8List> Function() _bytes;

  /// The zip's bytes: read from the link, or from the answer.
  Future<Uint8List> bytes() => _bytes();
}

/// The signed-in user's own account data: `client.account`. A person can
/// download a copy of everything the realm holds about them (documents, files
/// and account data, with a `README.html`): ask for it, follow it, and save it
/// when it is ready.
///
/// ```dart
/// await client.auth.reauth('password', password: password);
/// final job = await client.account.requestExport();   // {id, status: 'queued', …}
/// final ready = await client.account.waitForExport(job['id'] as String);
/// final zip = await (await client.account.downloadExport('${ready['id']}')).bytes();
/// ```
///
/// The realm must set `compliance.export: true` ([NotFoundError] otherwise).
/// A user may ask once every 24 hours: a second request is a
/// [RetryableError] whose `retryAfter` says how long to wait. When the package
/// is ready the user is also emailed a link to the app's download page.
final class Account {
  Account(this.client);

  final BackdClient<Object?> client;

  /// Asks for an export of the user's personal data. Needs a fresh proof: call
  /// `client.auth.reauth` first (a `reauth_required` error says so).
  Future<Map<String, Object?>> requestExport({RequestOptions? options}) async {
    final res = await client.request(RequestInit(
      method: 'POST',
      path: const ['_auth', 'me', 'export'],
      reauth: true,
      headers: options?.headers,
      retry: options?.retry,
      cache: false,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }

  /// The exports the user asked for, newest first.
  Future<List<Map<String, Object?>>> exports({RequestOptions? options}) async {
    final res = await client.request(RequestInit(
      method: 'GET',
      path: const ['_auth', 'me', 'exports'],
      headers: options?.headers,
      retry: options?.retry,
      cache: false,
    ));
    return [
      for (final e in (res.data as Map?)?['items'] as List? ?? const [])
        if (e is Map) Map<String, Object?>.from(e),
    ];
  }

  /// One export, for a page that waits for it: `step` says what it is doing
  /// while it runs.
  Future<Map<String, Object?>> export(String id, {RequestOptions? options}) async {
    final res = await client.request(RequestInit(
      method: 'GET',
      path: ['_auth', 'me', 'exports', id],
      headers: options?.headers,
      retry: options?.retry,
      cache: false,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }

  /// Polls an export until it is `ready`. A `failed` or `expired` one is a
  /// [StateError] (the user may ask again).
  Future<Map<String, Object?>> waitForExport(
    String id, {
    Duration pollInterval = const Duration(seconds: 2),
    Duration timeout = const Duration(minutes: 30),
    void Function(Map<String, Object?> export)? onProgress,
    RequestOptions? options,
  }) async {
    final end = DateTime.now().add(timeout);
    for (;;) {
      final e = await export(id, options: options);
      onProgress?.call(e);
      final status = e['status'];
      if (status == 'ready') return e;
      if (status == 'failed' || status == 'expired') {
        throw StateError('backd: the export $status'
            '${status == 'failed' ? ': ask for a new one' : ''}');
      }
      if (DateTime.now().add(pollInterval).isAfter(end)) {
        throw StateError('backd: the export is still $status after '
            '${timeout.inSeconds}s');
      }
      await Future<void>.delayed(pollInterval);
    }
  }

  /// Gives access to a ready export. [ConflictError] until it is ready, and
  /// `410 gone` once it has expired. A realm that delivers by link answers
  /// with `url` (open it, or `bytes()` reads it); one whose storage clients
  /// cannot reach sends the zip through the API, and `bytes()` reads it from
  /// the answer.
  Future<ExportDownload> downloadExport(String id, {RequestOptions? options}) async {
    final res = await client.request(RequestInit(
      method: 'GET',
      path: ['_auth', 'me', 'exports', id, 'download'],
      raw: true,
      headers: {'Accept': 'application/json', ...?options?.headers},
      retry: options?.retry,
      cache: false,
    ));
    final answer = res.response;
    if (answer == null) throw StateError('backd: no response');
    final type = answer.headers['content-type']?.first ?? '';
    if (!type.startsWith('application/json')) {
      final body = Uint8List.fromList(answer.body);
      return ExportDownload._(bytes: () async => body);
    }
    final link = Map<String, Object?>.from(jsonDecode(utf8.decode(answer.body)) as Map);
    final url = '${link['url']}';
    return ExportDownload._(
      url: url,
      expiresAt: link['expires_at'] as String?,
      bytes: () async {
        final r = await client.send(Uri.parse(url), method: 'GET');
        if (r.status >= 300) {
          throw StateError('backd: the export link answered ${r.status}');
        }
        return Uint8List.fromList(r.body);
      },
    );
  }
}
