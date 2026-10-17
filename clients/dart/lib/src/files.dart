import 'dart:async';
import 'dart:convert';
import 'dart:math' show min;
import 'dart:typed_data';

import 'package:crypto/crypto.dart';

import 'client.dart';
import 'data.dart';
import 'errors.dart';
import 'transport.dart';

/// One version of an image: `pending` (a worker will make it), `ready`
/// (made: `id`, `size`, `type`, `width`, `height`), `empty` (only functions
/// make it, none has yet), `skipped` (`reason` is `not_an_image` or
/// `unsupported_format`) or `failed` (`reason` is `too_large` or
/// `decode_error`).
final class VersionState {
  const VersionState({required this.status, this.reason, this.raw});

  factory VersionState.fromJson(Map<String, Object?> json) => VersionState(
        status: '${json['status'] ?? 'pending'}',
        reason: json['reason'] as String?,
        raw: json,
      );

  /// `pending`, `ready`, `empty`, `skipped` or `failed`.
  final String status;
  final String? reason;

  /// The raw state: `id`, `size`, `type`, `width`, `height`, `params`,
  /// `fingerprint`, `custom`, `generated_at` when made.
  final Map<String, Object?>? raw;

  bool get ready => status == 'ready';
}

/// What a document says about a made version of one of its files, to show
/// "processing…" or "no preview" and to reserve room for the picture.
final class VersionStatus {
  const VersionStatus(
      {required this.status, this.reason, this.width, this.height});

  /// `pending`, `ready`, `empty`, `skipped`, `failed` or `undeclared` (the
  /// file's details don't name it).
  final String status;
  final String? reason;
  final int? width;
  final int? height;

  bool get ready => status == 'ready';
}

/// What a document says about a made version of one of its files.
VersionStatus versionStatus(Map<String, Object?> file, String name) {
  final versions = file['versions'];
  final v =
      versions is Map && versions[name] is Map ? versions[name] as Map : null;
  if (v == null) return const VersionStatus(status: 'undeclared');
  return VersionStatus(
    status: '${v['status'] ?? 'pending'}',
    reason: v['reason'] as String?,
    width: (v['width'] as num?)?.toInt(),
    height: (v['height'] as num?)?.toInt(),
  );
}

/// A link to a file that works without credentials until it expires.
final class FileLink {
  const FileLink({required this.url, required this.expiresAt});

  factory FileLink.fromJson(Map<String, Object?> json) => FileLink(
        url: json['url'] as String? ?? '',
        expiresAt: json['expires_at'] as String? ?? '',
      );

  final String url;

  /// RFC3339 timestamp.
  final String expiresAt;
}

/// A downloaded file: the answer of [Files.get], already read.
final class FileContent {
  const FileContent({required this.response, this.file});

  /// The file's details as the document held them, when [Files.get] looked
  /// them up (no `fileId` given).
  final Map<String, Object?>? file;

  /// The answer: status, headers and body bytes.
  final TransportResponse response;

  /// The file's bytes.
  Uint8List get bytes => Uint8List.fromList(response.body);

  /// The file's bytes as UTF-8 text.
  String text() => utf8.decode(response.body);
}

/// A file for an upload: its bytes, plus a display [name] (sanitized by
/// backd; default `file`) and a [type] hint (backd detects it from the
/// content).
final class UploadData {
  const UploadData(this.bytes, {this.name, this.type});

  /// A plain string is UTF-8 text; bytes are sent as given.
  UploadData.of(Object data, {String? name, String? type})
      : this(
          data is String
              ? utf8.encode(data)
              : data is List<int>
                  ? data as Uint8List? ?? Uint8List.fromList(data)
                  : data is Uint8List
                      ? data
                      : throw ArgumentError.value(data, 'data',
                          'must be a String or a List<int>'),
          name: name,
          type: type,
        );

  final List<int> bytes;
  final String? name;
  final String? type;

  int get size => bytes.length;
}

/// The files of one document's file field:
/// `client.db(db).collection(name).files(id, field)`. Every operation is a
/// request to the field's `_files` routes, so the caller's access rules
/// apply as they do to any request.
final class Files {
  Files(this.client, this.collectionPath, this.id, this.field);

  final BackdClient<Object?> client;
  final List<String> collectionPath;
  final String id;
  final String field;

  List<String> get _path =>
      [...collectionPath, id, '_files', field];

  /// The files the document holds in the field (one at most, unless the
  /// field is `multiple`), as the document carries them. [NotFoundError] if
  /// the document doesn't exist or the caller may not read it.
  Future<List<Map<String, Object?>>> list({RequestOptions? options}) async {
    final res = await client.request(RequestInit(
      method: 'GET',
      path: [...collectionPath, id],
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    final v = res.data is Map ? (res.data as Map)[field] : null;
    if (v == null) return const [];
    final list = v is List ? v : [v];
    return [
      for (final f in list)
        if (f is Map) Map<String, Object?>.from(f),
    ];
  }

  /// The file id [fileId] names, or the one a single field holds when it is
  /// null.
  Future<({String fileId, Map<String, Object?>? file})> _which(
      String? fileId, RequestOptions? options) async {
    if (fileId != null) return (fileId: fileId, file: null);
    final files = await list(options: options);
    if (files.isEmpty) {
      throw NotFoundError(
          status: 404,
          code: 'not_found',
          message: 'the document holds no file in $field');
    }
    if (files.length > 1) {
      throw ArgumentError.value(field, 'field',
          'files($field): the field holds several files: say which one (a fileId)');
    }
    return (fileId: files.single['id'] as String? ?? '', file: files.single);
  }

  /// Downloads a file. Without [fileId] the one a single field holds.
  /// [NotFoundError] when there is none or the caller may not read the
  /// document; a [BackdException] with code `file_missing` when the storage
  /// no longer has it. [version] reads a made version of an image instead:
  /// `version_unavailable` when it isn't ready.
  Future<FileContent> get({
    String? fileId,
    String? version,
    RequestOptions? options,
  }) async {
    final found = await _which(fileId, options);
    final res = await client.request(RequestInit(
      method: 'GET',
      path: [..._path, found.fileId],
      query: {'version': version},
      raw: true,
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    final response = res.response;
    if (response == null) throw StateError('backd: no response');
    return FileContent(response: response, file: found.file);
  }

  /// Stores a file in the field: it replaces the one a single field holds
  /// and is added to a `multiple` field's. Resolves with the updated
  /// document, which holds the file's details. backd checks the document's
  /// `update` rule, the field's `max_size`, `types` and `max_files`, and
  /// detects the type from the content.
  Future<Doc> put(
    Object data, {
    String? name,
    String? type,
    Object? ifMatch,
    void Function(UploadProgress)? onProgress,
    RequestOptions? options,
  }) async {
    final upload = UploadData.of(data, name: name, type: type);
    final res = await client.request(RequestInit(
      method: 'POST',
      path: _path,
      query: {'name': upload.name},
      rawBody: upload.bytes,
      contentType: upload.type ?? 'application/octet-stream',
      headers: {
        ...?options?.headers,
        if (ifMatch != null) 'If-Match': ifMatchValue(ifMatch),
      },
      onUploadProgress: onProgress,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }

  /// Removes a file from the document (a new version), or, without [fileId],
  /// every file of the field. Resolves with the updated document.
  Future<Doc> delete({
    String? fileId,
    Object? ifMatch,
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'DELETE',
      path: fileId == null ? _path : [..._path, fileId],
      headers: {
        ...?options?.headers,
        if (ifMatch != null) 'If-Match': ifMatchValue(ifMatch),
      },
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }

  /// One of the file's declared versions (`versions:` in collection.yaml),
  /// to make again, generate with other parameters, or delete. They are
  /// requests that wait for a worker, so a function can use them within its
  /// time limit; the caller's `update` rule applies. Errors carry the reason
  /// as their `code`: `not_an_image`, `unsupported_format`, `too_large`,
  /// `decode_error`, `timeout` (422), `version_not_writable` (403),
  /// `version_timeout` (504, no worker).
  FileVersion version(String name, {String? fileId}) =>
      FileVersion._(this, name, fileId);

  /// A link to download the file without credentials, until it expires: for
  /// an app to open or show. Without [fileId] the one a single field holds.
  /// [version] links a made version of an image instead: `version_unavailable`
  /// when it isn't ready.
  Future<FileLink> link({
    String? fileId,
    String? version,
    RequestOptions? options,
  }) async {
    final found = await _which(fileId, options);
    final res = await client.request(RequestInit(
      method: 'GET',
      path: [..._path, found.fileId],
      query: {'link': 'json', 'version': version},
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return FileLink.fromJson(Map<String, Object?>.from(res.data as Map));
  }
}

/// A made version of one file ([Files.version]): make it again, make it with
/// other parameters, or drop it. Each waits for a worker to do it and
/// resolves with the version's state.
final class FileVersion {
  const FileVersion._(this.files, this.name, this.fileId);

  final Files files;
  final String name;
  final String? fileId;

  Future<List<String>> _path(RequestOptions? options) async => [
        ...files._path,
        (await files._which(fileId, options)).fileId,
        'versions',
        name,
      ];

  /// Again, with the parameters `collection.yaml` declares.
  Future<VersionState> regenerate({RequestOptions? options}) async {
    final res = await files.client.request(RequestInit(
      method: 'POST',
      path: await _path(options),
      headers: options?.headers,
      retry: options?.retry,
    ));
    return VersionState.fromJson(Map<String, Object?>.from(res.data as Map));
  }

  /// With other parameters (`max_width`, `max_height`, `fit`, `quality`,
  /// `format`, `upscale`): only a `writable` version allows it
  /// (`version_not_writable`).
  Future<VersionState> generate(Map<String, Object?> params,
      {RequestOptions? options}) async {
    final res = await files.client.request(RequestInit(
      method: 'POST',
      path: await _path(options),
      body: params,
      headers: options?.headers,
      retry: options?.retry,
    ));
    return VersionState.fromJson(Map<String, Object?>.from(res.data as Map));
  }

  /// Drops the copy: the version is `pending` again (a worker makes it) or
  /// `empty` (only functions make it).
  Future<VersionState> delete({RequestOptions? options}) async {
    final res = await files.client.request(RequestInit(
      method: 'DELETE',
      path: await _path(options),
      headers: options?.headers,
      retry: options?.retry,
    ));
    return VersionState.fromJson(Map<String, Object?>.from(res.data as Map));
  }
}

/// How a file field takes files (`GET …/_files/{field}`): to choose between
/// a proxy and a direct upload, and to refuse a file before sending it.
final class FileField {
  const FileField({
    required this.name,
    required this.upload,
    required this.download,
    required this.multiple,
    required this.maxFiles,
    required this.maxSize,
    required this.types,
    this.multipartAbove,
  });

  factory FileField.fromJson(Map<String, Object?> json) => FileField(
        name: json['name'] as String? ?? '',
        upload: json['upload'] as String? ?? 'proxy',
        download: json['download'] as String? ?? 'presigned',
        multiple: json['multiple'] as bool? ?? false,
        maxFiles: (json['max_files'] as num?)?.toInt() ?? 1,
        maxSize: (json['max_size'] as num?)?.toInt() ?? 0,
        types: [
          for (final t in json['types'] as List? ?? const []) '$t',
        ],
        multipartAbove: (json['multipart_above'] as num?)?.toInt(),
      );

  final String name;

  /// `proxy` or `direct`.
  final String upload;

  /// `presigned` or `proxy`.
  final String download;
  final bool multiple;

  /// 1 for a field that holds one file.
  final int maxFiles;

  /// Bytes per file.
  final int maxSize;

  /// Allowed content types; empty allows any.
  final List<String> types;

  /// A direct field that can hold more: files above this many bytes are sent
  /// in parts.
  final int? multipartAbove;
}

/// What a pending upload gives back: name [ref] in the field of a create,
/// `put` or `patch`.
final class PreparedUpload {
  const PreparedUpload({
    required this.id,
    required this.token,
    required this.ref,
    required this.expiresAt,
    required this.file,
  });

  final String id;
  final String token;

  /// `{ upload, token }`: what goes in the document's field.
  final Map<String, Object?> ref;

  /// RFC3339 timestamp: unused, it expires then.
  final String expiresAt;

  /// What backd stored: `name`, `size`, `type`, `sha256`.
  final Map<String, Object?> file;
}

/// Field configurations asked once per client and remembered: failures
/// aren't.
final _fields = Expando<Map<String, Future<FileField>>>();

/// A field's configuration, asked once per client and remembered.
Future<FileField> fileField(
  BackdClient<Object?> client,
  List<String> collectionPath,
  String field, {
  RequestOptions? options,
}) {
  var byKey = _fields[client];
  if (byKey == null) _fields[client] = byKey = {};
  final key = '${collectionPath.join('/')}/$field';
  var found = byKey[key];
  if (found == null) {
    found = client
        .request(RequestInit(
          method: 'GET',
          path: [...collectionPath, '_files', field],
          headers: options?.headers,
          retry: options?.retry,
        ))
        .then(
            (r) => FileField.fromJson(Map<String, Object?>.from(r.data as Map)));
    // A failure isn't remembered.
    unawaited(found.catchError((Object _) {
      byKey?.remove(key);
      return FileField(
          name: '',
          upload: '',
          download: '',
          multiple: false,
          maxFiles: 0,
          maxSize: 0,
          types: const []);
    }));
    byKey[key] = found;
  }
  return found;
}

/// The size of the parts of a file sent in parts: at least 64 MiB, and a
/// multiple of 16 MiB when the file needs more to stay within 1000 parts.
/// backd checks it, so it is the same on both sides.
int partSizeFor(int size) {
  const mib = 1024 * 1024;
  const maxParts = 1000;
  final need = (((size + maxParts - 1) ~/ maxParts) + 16 * mib - 1) ~/
      (16 * mib) *
      16 *
      mib;
  return need > 64 * mib ? need : 64 * mib;
}

/// Sends a file to the signed link of a direct upload: the headers the link
/// names, and no credentials of backd's.
Future<void> _putToBucket(
  BackdClient<Object?> client,
  Uri url,
  Map<String, Object?> start, {
  List<int>? body,
  void Function(UploadProgress)? onProgress,
}) async {
  final headers = start['headers'] is Map
      ? {
          for (final e in (start['headers'] as Map).entries)
            '${e.key}': '${e.value}',
        }
      : const <String, String>{};
  final TransportResponse res;
  try {
    res = await client.send(
      url,
      method: 'PUT',
      headers: headers,
      body: body,
      onUploadProgress: onProgress,
    );
  } catch (cause) {
    if (cause is BackdException) rethrow;
    throw NetworkError(
        status: 0,
        code: 'network_error',
        message: 'network error: $cause',
        cause: cause);
  }
  if (res.status < 200 || res.status >= 300) {
    throw BackdException(
        status: res.status,
        code: 'upload_failed',
        message: 'the storage refused the file (${res.status}): is the '
            "bucket's CORS set for this origin, and was the file changed "
            'after it was declared?');
  }
}

/// Sends a file in parts, each to its own signed link: a few at a time, a
/// part that fails to go through is sent again (the others are not).
/// Progress is that of the whole file.
Future<void> _putParts(
  BackdClient<Object?> client,
  Map<String, Object?> start,
  List<int> bytes, {
  void Function(UploadProgress)? onProgress,
}) async {
  const partAttempts = 4;
  const partConcurrency = 3;
  final parts = [
    for (final p in start['parts'] as List? ?? const [])
      Map<String, Object?>.from(p as Map),
  ];
  final partSize = (start['part_size'] as num?)?.toInt() ?? 0;
  final sent = List<int>.filled(parts.length, 0);
  void report() =>
      onProgress?.call(UploadProgress(
          loaded: sent.fold(0, (a, b) => a + b), total: bytes.length));
  var next = 0;
  Future<void> worker() async {
    while (next < parts.length) {
      final i = next++;
      final part = parts[i];
      final size = (part['size'] as num?)?.toInt() ?? 0;
      final begin = i * partSize;
      final body = bytes.sublist(begin, min(begin + size, bytes.length));
      for (var attempt = 1;; attempt++) {
        try {
          await _putToBucket(client, Uri.parse('${part['url']}'), part,
              body: body,
              onProgress: onProgress == null
                  ? null
                  : (p) {
                      sent[i] = p.loaded;
                      report();
                    });
          break;
        } catch (e) {
          final retryable = e is NetworkError ||
              (e is BackdException && e.status >= 500);
          if (!retryable || attempt >= partAttempts) rethrow;
          sent[i] = 0;
          await Future<void>.delayed(
              Duration(milliseconds: 500 << (attempt - 1)));
        }
      }
      sent[i] = size;
      report();
    }
  }

  await Future.wait(
      List.generate(min(partConcurrency, parts.length), (_) => worker()));
}

/// Declares a file, sends it to the bucket and completes: the three steps of
/// a direct upload.
Future<({Map<String, Object?> start, Map<String, Object?> answer})> _direct(
  BackdClient<Object?> client,
  List<String> collectionPath,
  List<String> startPath,
  FileField info,
  UploadData file, {
  Object? ifMatch,
  void Function(UploadProgress)? onProgress,
  RequestOptions? options,
}) async {
  final name = file.name ?? 'file';
  final type = file.type ?? 'application/octet-stream';
  final multipart =
      info.multipartAbove != null && file.size > info.multipartAbove!;
  final Map<String, Object?> declared;
  if (multipart) {
    final partSize = partSizeFor(file.size);
    declared = {
      'part_sha256': [
        for (var at = 0; at < file.size; at += partSize)
          sha256.convert(file.bytes.sublist(at, min(at + partSize, file.size)))
              .toString(),
      ],
    };
  } else {
    declared = {'sha256': sha256.convert(file.bytes).toString()};
  }
  final headers = {
    ...?options?.headers,
    if (ifMatch != null) 'If-Match': ifMatchValue(ifMatch),
  };
  final start = Map<String, Object?>.from((await client.request(RequestInit(
    method: 'POST',
    path: startPath,
    body: {'name': name, 'size': file.size, 'type': type, ...declared},
    headers: headers,
    retry: options?.retry,
  )))
      .data as Map);
  if (start['multipart'] == true) {
    await _putParts(client, start, file.bytes, onProgress: onProgress);
  } else {
    await _putToBucket(client, Uri.parse('${start['url']}'), start,
        body: file.bytes, onProgress: onProgress);
  }
  final answer = Map<String, Object?>.from((await client.request(RequestInit(
    method: 'POST',
    path: [
      ...collectionPath,
      '_files',
      info.name,
      'uploads',
      '${start['upload_id']}',
      'complete',
    ],
    body: {'upload_token': start['upload_token']},
    headers: headers,
    retry: options?.retry,
  )))
      .data as Map);
  return (start: start, answer: answer);
}

/// Uploads a file into a document's file field, whichever way the field
/// takes it: streamed through backd (`upload: proxy`), or straight to the
/// bucket with a signed link that backd verifies afterwards (`upload:
/// direct`, with the SHA-256 computed as the file is read). Resolves with
/// the updated document.
Future<Doc> uploadFile(
  BackdClient<Object?> client,
  List<String> collectionPath,
  String id,
  String field,
  Object file, {
  String? name,
  String? type,
  Object? ifMatch,
  void Function(UploadProgress)? onProgress,
  RequestOptions? options,
}) async {
  final data = UploadData.of(file, name: name, type: type);
  final info = await fileField(client, collectionPath, field);
  if (info.upload == 'direct') {
    final done = await _direct(
      client,
      collectionPath,
      [...collectionPath, id, '_files', field, 'uploads'],
      info,
      data,
      ifMatch: ifMatch,
      onProgress: onProgress,
      options: options,
    );
    return done.answer;
  }
  return Files(client, collectionPath, id, field).put(data.bytes,
      name: data.name,
      type: data.type,
      ifMatch: ifMatch,
      onProgress: onProgress,
      options: options);
}

/// Uploads a file for a document that doesn't exist yet (a pending upload),
/// whichever way the field takes it, and answers what to name in the field
/// of the create: `{ upload: id, token }`.
Future<PreparedUpload> prepareUpload(
  BackdClient<Object?> client,
  List<String> collectionPath,
  String field,
  Object file, {
  String? name,
  String? type,
  void Function(UploadProgress)? onProgress,
  RequestOptions? options,
}) async {
  final data = UploadData.of(file, name: name, type: type);
  final info = await fileField(client, collectionPath, field);
  final Map<String, Object?> start;
  final Map<String, Object?> stored;
  if (info.upload == 'direct') {
    final done = await _direct(
      client,
      collectionPath,
      [...collectionPath, '_files', field, 'uploads'],
      info,
      data,
      onProgress: onProgress,
      options: options,
    );
    start = done.start;
    stored = done.answer['file'] is Map
        ? Map<String, Object?>.from(done.answer['file'] as Map)
        : const {};
  } else {
    start = Map<String, Object?>.from((await client.request(RequestInit(
      method: 'POST',
      path: [...collectionPath, '_files', field, 'uploads'],
      query: {'name': data.name ?? 'file'},
      rawBody: data.bytes,
      contentType: data.type ?? 'application/octet-stream',
      onUploadProgress: onProgress,
      headers: options?.headers,
      retry: options?.retry,
    )))
        .data as Map);
    stored = start['file'] is Map
        ? Map<String, Object?>.from(start['file'] as Map)
        : const {};
  }
  return PreparedUpload(
    id: '${start['upload_id'] ?? ''}',
    token: '${start['upload_token'] ?? ''}',
    ref: {'upload': start['upload_id'], 'token': start['upload_token']},
    expiresAt: '${start['expires_at'] ?? ''}',
    file: stored,
  );
}

/// A link to a file, for an app to show or open: `{ url, expiresAt }`. With
/// [version] it is a link to that made version of an image, or null while
/// the version isn't ready (pending, skipped, failed, or not made by a
/// worker yet): show a placeholder, and read the document ([versionStatus])
/// to tell why.
Future<FileLink?> fileUrl(
  BackdClient<Object?> client,
  List<String> collectionPath,
  String id,
  String field,
  String fileId, {
  String? version,
  RequestOptions? options,
}) async {
  try {
    return await Files(client, collectionPath, id, field)
        .link(fileId: fileId, version: version, options: options);
  } on BackdException catch (e) {
    if (version != null && e.code == 'version_unavailable') return null;
    rethrow;
  }
}
