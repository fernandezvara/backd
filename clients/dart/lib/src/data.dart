import 'dart:convert';

import 'client.dart';
import 'files.dart' as fileio;
import 'functions.dart';
import 'transport.dart' show UploadProgress;
import 'errors.dart';

/// A stored document: the collection's fields plus `id` and `_meta`
/// (server-owned: `created_at`, `updated_at`, `version`, `owner`, `org`,
/// `team`, `score`, the soft-delete fields) and `_expanded` when the read
/// asked for `expand`. Generated realm types give the fields their classes.
typedef Doc = Map<String, Object?>;

/// Which soft-deleted documents a read of a `soft_delete` collection
/// returns.
enum DeletedDocs {
  /// The trash: only the deleted documents.
  only,

  /// Deleted and live documents.
  include,
}

/// A page of documents, as the API returns it.
final class Page<T> {
  const Page({
    required this.items,
    required this.limit,
    required this.skip,
    required this.hasMore,
    this.nextCursor,
    this.total,
  });

  factory Page.fromJson(
    Map<String, Object?> json,
    T Function(Object? item) decode,
  ) =>
      Page(
        items: [
          for (final item in json['items'] as List? ?? const [])
            decode(item),
        ],
        limit: json['limit'] as int? ?? 0,
        skip: json['skip'] as int? ?? 0,
        hasMore: json['has_more'] as bool? ?? false,
        nextCursor: json['next_cursor'] as String?,
        total: json['total'] as int?,
      );

  final List<T> items;
  final int limit;
  final int skip;
  final bool hasMore;

  /// With [hasMore]: pass it as `after` to get the next page. Absent when the
  /// `orderBy` can't be followed by a cursor (arrays, objects, mixed types):
  /// use `skip` then.
  final String? nextCursor;

  /// With `count: true`; counts the whole list, not what is after `after`.
  final int? total;
}

/// One write for [Database.batch], applied atomically with the others.
sealed class BatchOperation {
  const BatchOperation();

  Map<String, Object?> toJson();
}

/// A `create` in the batch: `collection` and `document` (with `id` when the
/// collection declares `custom_id`).
final class BatchCreate extends BatchOperation {
  const BatchCreate({required this.collection, required this.document});

  final String collection;
  final Doc document;

  @override
  Map<String, Object?> toJson() =>
      {'op': 'create', 'collection': collection, 'document': document};
}

/// A `replace` in the batch.
final class BatchReplace extends BatchOperation {
  const BatchReplace({
    required this.collection,
    required this.id,
    required this.document,
    this.ifMatch,
  });

  final String collection;
  final String id;
  final Doc document;
  final Object? ifMatch;

  @override
  Map<String, Object?> toJson() => {
        'op': 'replace',
        'collection': collection,
        'id': id,
        'document': document,
        if (ifMatch != null) 'if_match': ifMatchValue(ifMatch!),
      };
}

/// A `patch` in the batch: [patch] is a JSON Merge Patch.
final class BatchPatch extends BatchOperation {
  const BatchPatch({
    required this.collection,
    required this.id,
    required this.patch,
    this.ifMatch,
  });

  final String collection;
  final String id;
  final Doc patch;
  final Object? ifMatch;

  @override
  Map<String, Object?> toJson() => {
        'op': 'patch',
        'collection': collection,
        'id': id,
        'patch': patch,
        if (ifMatch != null) 'if_match': ifMatchValue(ifMatch!),
      };
}

/// A `delete` in the batch.
final class BatchDelete extends BatchOperation {
  const BatchDelete({
    required this.collection,
    required this.id,
    this.ifMatch,
  });

  final String collection;
  final String id;
  final Object? ifMatch;

  @override
  Map<String, Object?> toJson() => {
        'op': 'delete',
        'collection': collection,
        'id': id,
        if (ifMatch != null) 'if_match': ifMatchValue(ifMatch!),
      };
}

/// `3` → `"3"`; strings (`*`, `"3"`, `"2", "3"`) are sent as given. Builds the
/// `If-Match` header of a write and the `if_match` of a [BatchOperation].
String ifMatchValue(Object version) => version is num ? '"$version"' : '$version';

/// One group of an [Collection.aggregate]: [key] has an entry per `groupBy`
/// field (`null` for documents without a value), and [metrics] the results
/// named `<metric>_<field>`: `count`, `sum_total`, `avg_total`,
/// `count_distinct__meta.owner`. Sums and averages are exact decimal strings.
final class AggregateGroup {
  const AggregateGroup({required this.key, required this.metrics});

  /// One entry of `key` per groupBy field; everything else is a metric.
  factory AggregateGroup.fromJson(Map<String, Object?> json) {
    final key = json['key'] is Map
        ? Map<String, Object?>.from(json['key'] as Map)
        : const <String, Object?>{};
    final metrics = Map<String, Object?>.from(json)..remove('key');
    return AggregateGroup(key: key, metrics: metrics);
  }

  final Map<String, Object?> key;
  final Map<String, Object?> metrics;

  /// The `count` metric, when counted.
  int? get count => metrics['count'] as int?;
}

/// The answer of [Collection.aggregate].
final class AggregateResult {
  const AggregateResult({required this.groups, required this.totalGroups});

  factory AggregateResult.fromJson(Map<String, Object?> json) => AggregateResult(
        groups: [
          for (final g in json['groups'] as List? ?? const [])
            if (g is Map) AggregateGroup.fromJson(Map<String, Object?>.from(g)),
        ],
        totalGroups: json['total_groups'] as int? ?? 0,
      );

  final List<AggregateGroup> groups;

  /// How many groups there were before `limit`.
  final int totalGroups;
}

/// A database of the realm: `client.db(name)`.
final class Database {
  Database(this.client, this.name);

  final BackdClient<Object?> client;
  final String name;

  /// A collection of this database.
  Collection collection(String name) => Collection(client, this.name, name);

  /// Creates, replaces, patches and deletes documents across this database's
  /// collections in one MongoDB transaction: either every operation applies,
  /// or none does (up to 100 operations). Throws a [BackdException] naming
  /// the first operation that failed; a version mismatch (`ifMatch`) is a
  /// [VersionMismatchError], same as a single write.
  ///
  /// `idempotencyKey` makes a retry safe: the server remembers the answer of
  /// the first request with a key for 24 hours. `team` scopes the creates
  /// (see [Collection.inTeam]).
  ///
  /// Runs the operations atomically: all of them or none.
  Future<List<Doc>> batch(
    List<BatchOperation> operations, {
    String? idempotencyKey,
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'POST',
      path: [name, '_batch'],
      body: {'operations': [for (final op in operations) op.toJson()]},
      headers: {
        ...?options?.headers,
        if (idempotencyKey != null) 'Idempotency-Key': idempotencyKey,
      },
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    final list = res.data is Map ? (res.data as Map)['results'] : res.data;
    return [
      for (final d in list as List? ?? const [])
        if (d is Map) Map<String, Object?>.from(d),
    ];
  }

  /// Calls a function. Returns its output directly for a `sync` function;
  /// for `async`, a [Job] handle to poll instead of the output
  /// (`job.status()`, or `job.wait()` for the output). A function's own
  /// error (`ctx.error(status, code, message)`) arrives as a
  /// [BackdException] with that status and code — for `async`, only once
  /// `job.wait()` resolves, not from this call itself.
  ///
  /// With [respondAsync] (`Prefer: respond-async`) a `sync` function is
  /// queued as a job too, and this resolves with its [Job] instead of the
  /// output: for calls that can take long, or that nobody waits for. It
  /// runs once, with the function's own limits.
  Future<Object?> fn(
    String name,
    Object? input, {
    String? idempotencyKey,
    bool respondAsync = false,
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'POST',
      path: [this.name, '_func', name],
      body: input,
      headers: {
        ...?options?.headers,
        if (idempotencyKey != null) 'Idempotency-Key': idempotencyKey,
        if (respondAsync) 'Prefer': 'respond-async',
      },
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    if (res.status == 202) {
      return Job(client, this.name,
          Map<String, Object?>.from(res.data as Map));
    }
    return res.data;
  }
}

/// A collection: `client.db(db).collection(name)`. Untyped, documents are
/// `Map<String, Object?>`; the generated realm types (issue #543) bind them
/// to their classes.
final class Collection {
  /// [team] scopes lists and creates to a team of the session's organization
  /// (see [inTeam]); a session works in the one organization it was opened
  /// for, so there is none to name.
  const Collection(this.client, this.dbName, this.name, {this.team});

  final BackdClient<Object?> client;
  final String dbName;
  final String name;
  final String? team;

  List<String> get _path => [dbName, name];

  /// The same collection scoped to a team of the session's organization:
  /// [create] puts documents in that team and [list] and [iterate] show only
  /// its documents. The caller must belong to the team or hold a team role in
  /// it: a create answers [NotTeamMemberError] otherwise.
  Collection inTeam(String team) =>
      Collection(client, dbName, name, team: team);

  /// One page of documents the caller may read.
  ///
  /// [where] is a filter in backd's query language, e.g.
  /// `{price: {$lt: 20}}` or `{$or: [...]}` — a `Map`, or a JSON string sent
  /// as-is. [orderBy] is a field or list of fields, `-` prefix for
  /// descending (`'-_meta.created_at'`). [after] is the [Page.nextCursor] of
  /// the previous page (same `where` and `orderBy`); it can't be combined
  /// with `skip`. `count: true` also returns [Page.total]. [fileLinks] adds
  /// `url` and `expires_at` to every file of every document. [language] is
  /// the language a `$search` is stemmed in. [expand] names the references
  /// to return in `_expanded` — `'author(name),tags'`, or built with
  /// `expand()`. [deleted] reads the trash of a `soft_delete` collection.
  Future<Page<Doc>> list({
    Object? where,
    Object? orderBy,
    int? limit,
    int? skip,
    String? after,
    bool? count,
    bool? fileLinks,
    String? language,
    String? expand,
    DeletedDocs? deleted,
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'GET',
      path: _path,
      query: {
        ..._listQuery(
          where: where,
          orderBy: orderBy,
          limit: limit,
          skip: skip,
          after: after,
          count: count,
          fileLinks: fileLinks,
          language: language,
          expand: expand,
          deleted: deleted,
        ),
        ...scopeQuery(team),
      },
      retry: options?.retry,
      headers: options?.headers,
      cache: options?.cache ?? true,
    ));
    return Page.fromJson(
      res.data as Map<String, Object?>,
      (item) => item is Map ? Map<String, Object?>.from(item) : const {},
    );
  }

  /// Every matching document, fetching pages as needed:
  /// `await for (final doc in posts.iterate(where: …))`. Pages follow a
  /// cursor ([Page.nextCursor]), so documents created, changed or deleted
  /// meanwhile never shift a page, and it is as fast at the end as at the
  /// start. An `orderBy` on arrays, objects or mixed types has no cursor:
  /// those lists are fetched by offset instead, where documents created or
  /// deleted meanwhile can be skipped or repeated.
  ///
  /// [limit] is the page size (default 100).
  Stream<Doc> iterate({
    Object? where,
    Object? orderBy,
    int? limit,
    String? after,
    bool? fileLinks,
    String? language,
    String? expand,
    DeletedDocs? deleted,
    RequestOptions? options,
  }) async* {
    final pageSize = limit ?? 100;
    var cursor = after;
    var hasCursor = after != null;
    for (var skip = 0; ; skip += pageSize) {
      // With a cursor the next request carries it; without one, the offset.
      final page = await list(
        where: where,
        orderBy: orderBy,
        limit: pageSize,
        skip: hasCursor ? null : skip,
        after: cursor,
        fileLinks: fileLinks,
        language: language,
        expand: expand,
        deleted: deleted,
        options: options,
      );
      for (final doc in page.items) {
        yield doc;
      }
      if (!page.hasMore) return;
      if (page.nextCursor == null) {
        if (hasCursor) {
          throw StateError(
              'backd: the list has no next_cursor to continue after');
        }
      } else {
        cursor = page.nextCursor;
        hasCursor = true;
      }
    }
  }

  /// Summarizes the documents the caller may read: counts and measures
  /// grouped by the fields the collection declares in `aggregate:`. The read
  /// rules apply before grouping, so a total never includes a document a
  /// list would hide.
  ///
  /// ```dart
  /// final result = await orders.aggregate(
  ///   groupBy: ['category', 'paid_at:month'],
  ///   tz: 'Europe/Madrid',
  ///   metrics: ['count', 'sum:total'],
  ///   orderBy: '-sum_total',
  ///   limit: 20,
  /// );
  /// result.groups[0].key       // {category: 'shoes', 'paid_at:month': '2026-09'}
  /// result.groups[0].metrics['sum_total'] // '18234.5' (an exact decimal string)
  /// ```
  ///
  /// [AggregateNotEnabledError] when the collection declares no `aggregate:`,
  /// [InvalidAggregateError] for a wrong parameter and
  /// [AggregateTooExpensiveError] when it needs more than allowed.
  ///
  /// [groupBy] takes up to 3 declared fields; a date takes a bucket:
  /// `'paid_at:month'` (`day`, `week`, `month`, `year`). Without it, one
  /// group with an empty key (a counter). [metrics] are `count` (the
  /// default), `count_distinct:<field>`, `sum:<field>`, `avg:<field>`,
  /// `min:<field>`, `max:<field>`; a single string or a list. [tz] is the
  /// IANA zone of the date buckets (default `UTC`); [limit] the groups
  /// returned (default 100, at most the collection's maximum).
  Future<AggregateResult> aggregate({
    Object? where,
    Object? groupBy,
    Object? metrics,
    String? tz,
    Object? orderBy,
    int? limit,
    String? language,
    RequestOptions? options,
  }) async {
    String? listOf(Object? v) =>
        v == null ? null : (v is List ? v.join(',') : '$v');
    final res = await client.request(RequestInit(
      method: 'GET',
      path: [..._path, '_aggregate'],
      query: _compactQuery({
        'where': switch (where) {
          null => null,
          String() => where,
          _ => jsonEncode(where),
        },
        'group_by': listOf(groupBy),
        'metrics': listOf(metrics),
        'tz': tz,
        'order_by': listOf(orderBy),
        'limit': limit,
        'language': language,
        ...scopeQuery(team),
      }),
      // Aggregations never share the read cache: `_aggregate` is not
      // cacheable anyway.
      retry: options?.retry,
      headers: options?.headers,
      cache: options?.cache ?? true,
    ));
    return AggregateResult.fromJson(res.data as Map<String, Object?>);
  }

  /// Full-text search, best match first: a [list] whose `where` carries
  /// [text] as `$search` and whose order is `_relevance`, with each
  /// document's score in `_meta.score`. [text] is words, `"quoted phrases"`
  /// and `-excluded` words. Pages with [skip], through the first 1,000
  /// results. [SearchNotEnabledError] when the collection has no text index,
  /// [SearchTooExpensiveError] when it needs more time than allowed.
  ///
  /// ```dart
  /// final page = await articles.search(
  ///   'red "running shoes" -kids',
  ///   where: {'status': 'published'},
  ///   limit: 10,
  /// );
  /// ```
  Future<Page<Doc>> search(
    String text, {
    Object? where,
    int? limit,
    int? skip,
    bool? count,
    bool? fileLinks,
    String? language,
    String? expand,
    DeletedDocs? deleted,
    RequestOptions? options,
  }) {
    if (text.trim().isEmpty) {
      throw ArgumentError.value(
          text, 'text', 'backd: search needs the text to search for');
    }
    final base = switch (where) {
      null => <String, Object?>{},
      String() => Map<String, Object?>.from(jsonDecode(where) as Map),
      Map() => Map<String, Object?>.from(where),
      _ => throw ArgumentError.value(where, 'where', 'must be a Map or a JSON string'),
    };
    return list(
      where: {...base, '\$search': text},
      orderBy: '_relevance',
      limit: limit,
      skip: skip,
      count: count,
      fileLinks: fileLinks,
      language: language,
      expand: expand,
      deleted: deleted,
      options: options,
    );
  }

  /// The files a document holds in a file field (`files:` in
  /// collection.yaml): `list`, `get`, `put`, `delete`, `version` and `link`.
  /// They are the `_files` routes of the API, under the caller's own access
  /// rules.
  fileio.Files files(String id, String field) => fileio.Files(client, _path, id, field);

  /// How a file field takes files: `upload` (`proxy` or `direct`),
  /// `maxSize`, `types`, `multiple` and `maxFiles`. Asked once per client
  /// and remembered.
  Future<fileio.FileField> field(String field, {RequestOptions? options}) =>
      fileio.fileField(client, _path, field, options: options);

  /// Uploads a file into a document's file field, whichever way the field
  /// takes it (through backd, or straight to the bucket). Resolves with the
  /// updated document.
  Future<Doc> uploadFile(
    String id,
    String field,
    Object file, {
    String? name,
    String? type,
    Object? ifMatch,
    void Function(UploadProgress)? onProgress,
    RequestOptions? options,
  }) =>
      fileio.uploadFile(client, _path, id, field, file,
          name: name,
          type: type,
          ifMatch: ifMatch,
          onProgress: onProgress,
          options: options);

  /// Uploads a file for a document that doesn't exist yet. Name
  /// `PreparedUpload.ref` in the field of a `create`, `put` or `patch`.
  Future<fileio.PreparedUpload> prepareUpload(
    String field,
    Object file, {
    String? name,
    String? type,
    void Function(UploadProgress)? onProgress,
    RequestOptions? options,
  }) =>
      fileio.prepareUpload(client, _path, field, file,
          name: name, type: type, onProgress: onProgress, options: options);

  /// A link to a file, to show or open: `{ url, expiresAt }`. It works
  /// without credentials until it expires. With [version] it is null while
  /// the version isn't ready. Pass the file's details as [file] and the
  /// address of a public file is answered without a request.
  Future<fileio.FileLink?> fileUrl(
    String id,
    String field,
    String fileId, {
    String? version,
    Map<String, Object?>? file,
    RequestOptions? options,
  }) =>
      fileio.fileUrl(client, _path, id, field, fileId,
          version: version, file: file, options: options);

  /// A fresh link to download a file, for the app to open (with
  /// `url_launcher`, a browser, or an HTTP client): the same
  /// `{ url, expiresAt }` as [fileUrl], never null. The link works without
  /// credentials until it expires, so fetch it when the person asks.
  Future<fileio.FileLink> downloadFile(
    String id,
    String field,
    String fileId, {
    RequestOptions? options,
  }) async =>
      (await fileio.fileUrl(client, _path, id, field, fileId, options: options))!;

  /// Removes one file from a document. Resolves with the updated document.
  /// `files(id, field).delete()` clears the whole field.
  Future<Doc> deleteFile(
    String id,
    String field,
    String fileId, {
    Object? ifMatch,
    RequestOptions? options,
  }) {
    if (fileId.isEmpty) {
      throw ArgumentError.value(fileId, 'fileId',
          'the id of the file to remove is required (files(id, field).delete() clears the field)');
    }
    return files(id, field).delete(fileId: fileId, ifMatch: ifMatch, options: options);
  }

  /// One document; [NotFoundError] if it doesn't exist or the caller may not
  /// read it. A soft-deleted document is only found with [deleted] `only` or
  /// `include`.
  /// [fileLinks] adds `url` and `expires_at` to every file the document
  /// holds: for content shown right away (a gallery, a preview). They expire;
  /// for a download button, `downloadFile` gets a fresh link.
  /// [expand] returns the declared references named in it in `_expanded` (see
  /// `expand()`).
  Future<Doc> get(
    String id, {
    DeletedDocs? deleted,
    bool? fileLinks,
    String? expand,
    RequestOptions? options,
  }) async =>
      (await client.request(RequestInit(
        method: 'GET',
        path: [..._path, id],
        query: _compactQuery({
          'deleted': deleted?.name,
          'file_links': fileLinks == true ? true : null,
          'expand': expand,
        }),
        retry: options?.retry,
        headers: options?.headers,
        cache: options?.cache ?? true,
      )))
          .data as Doc;

  /// Creates a document. `_meta` is set by the server, and so is `id`, unless
  /// the collection declares `custom_id`: then [doc] carries the `id`
  /// (`400 id_required` otherwise; `409 id_taken` if it exists). With an
  /// [idempotencyKey], a repeated request returns the document created by
  /// the first one; the client also retries such a request itself after a
  /// network error, `429` or `503` (see [RetryOptions]).
  /// [team] overrides the scope of [inTeam] for this create.
  Future<Doc> create(
    Doc doc, {
    String? idempotencyKey,
    String? team,
    RequestOptions? options,
  }) async {
    final scopedTeam = team ?? this.team;
    return (await client.request(RequestInit(
      method: 'POST',
      path: _path,
      body: doc,
      query: scopeQuery(scopedTeam),
      headers: {
        ...?options?.headers,
        if (idempotencyKey != null) 'Idempotency-Key': idempotencyKey,
      },
      retry: options?.retry,
      cache: options?.cache ?? true,
    )))
        .data as Doc;
  }

  /// Replaces the whole document: fields not in [doc] are removed.
  /// [ifMatch] makes the write fail with a [VersionMismatchError] unless the
  /// document is still at that version (`3`, `'*'`, `'"2", "3"'`).
  Future<Doc> replace(
    String id,
    Doc doc, {
    Object? ifMatch,
    RequestOptions? options,
  }) async =>
      (await client.request(RequestInit(
        method: 'PUT',
        path: [..._path, id],
        body: doc,
        headers: _writeHeaders(ifMatch, options),
        retry: options?.retry,
        cache: options?.cache ?? true,
      )))
          .data as Doc;

  /// Updates some fields (JSON Merge Patch): fields in [patch] are set,
  /// nested objects are merged, and `null` removes a field.
  Future<Doc> patch(
    String id,
    Doc patch, {
    Object? ifMatch,
    RequestOptions? options,
  }) async =>
      (await client.request(RequestInit(
        method: 'PATCH',
        path: [..._path, id],
        body: patch,
        contentType: 'application/merge-patch+json',
        headers: _writeHeaders(ifMatch, options),
        retry: options?.retry,
        cache: options?.cache ?? true,
      )))
          .data as Doc;

  /// Deletes a document. In a collection with `soft_delete` it is marked
  /// deleted and can be restored; `purge: true` removes it for good (under
  /// the `purge` rule).
  Future<void> delete(
    String id, {
    bool? purge,
    Object? ifMatch,
    RequestOptions? options,
  }) =>
      client.request(RequestInit(
        method: 'DELETE',
        path: [..._path, id],
        query: _compactQuery({'purge': purge == true ? true : null}),
        headers: _writeHeaders(ifMatch, options),
        retry: options?.retry,
        cache: options?.cache ?? true,
      ));

  /// Brings a soft-deleted document back, as it was, at the next version.
  /// [NotFoundError] if it isn't in the trash or the caller may not see it
  /// there.
  Future<Doc> restore(
    String id, {
    Object? ifMatch,
    RequestOptions? options,
  }) async =>
      (await client.request(RequestInit(
        method: 'POST',
        path: [..._path, id, 'restore'],
        headers: _writeHeaders(ifMatch, options),
        retry: options?.retry,
        cache: options?.cache ?? true,
      )))
          .data as Doc;
}

/// The `team` query parameter of a scope, when it is set.
Map<String, String> scopeQuery(String? team) => {
      if (team != null) 'team': team,
    };

Map<String, Object?> _compactQuery(Map<String, Object?> query) =>
    {for (final e in query.entries) if (e.value != null) e.key: e.value};

/// The query of a list request.
Map<String, Object?> _listQuery({
  Object? where,
  Object? orderBy,
  int? limit,
  int? skip,
  String? after,
  bool? count,
  bool? fileLinks,
  String? language,
  String? expand,
  DeletedDocs? deleted,
}) =>
    _compactQuery({
      'where': switch (where) {
        null => null,
        String() => where,
        _ => jsonEncode(where),
      },
      'order_by': switch (orderBy) {
        null => null,
        List() => orderBy.join(','),
        _ => '$orderBy',
      },
      'limit': limit,
      'skip': skip,
      'after': after,
      'count': count == true ? true : null,
      'deleted': deleted?.name,
      'file_links': fileLinks == true ? true : null,
      'expand': expand,
      'language': language,
    });

/// `ifMatch` plus the caller's headers: the If-Match of a write.
Map<String, String>? _writeHeaders(Object? ifMatch, RequestOptions? options) {
  if (ifMatch == null && options?.headers == null) return null;
  return {
    ...?options?.headers,
    if (ifMatch != null) 'If-Match': ifMatchValue(ifMatch),
  };
}

/// One entry of an `expand`: a reference name (or `name.nested` for a second
/// level), or `('name', ['fields'])` for the fields to return.
///
/// ```dart
/// posts.list(expand: expand([('author', ['name', 'avatar']), 'tags']))
/// // sends expand=author(name,avatar),tags
/// ```
typedef ExpandPart = Object; // String | (String, List<String>)

final _expandName = RegExp(r'^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$');

/// Builds the `expand` option of a read: the references to return next to the
/// result, in `_expanded`, with the fields to return of each. Nothing is
/// expanded unless you pass this (or a string) as `expand`; the client never
/// adds it by itself.
///
/// ```dart
/// final posts = await db.collection('posts').list(
///   expand: expand([('author', ['name', 'avatar']), 'tags']),
/// );
/// ```
String expand(List<ExpandPart> parts) => parts
    .map((part) {
      final (name, fields) = switch (part) {
        String n => (n, const <String>[]),
        (String n, List<Object?> f) => (n, f.map((x) => '$x').toList()),
        _ => throw ArgumentError.value(part, 'part',
            'is not a reference name to expand: a String or a (name, fields) record'),
      };
      if (!_expandName.hasMatch(name)) {
        throw ArgumentError.value(
            name, 'expand', 'is not a reference name to expand');
      }
      if (fields.isEmpty) return name;
      for (final f in fields) {
        if (!_expandName.hasMatch(f)) {
          throw ArgumentError.value(fields, 'expand',
              'the fields of $name must be a non-empty list of field names');
        }
      }
      return '$name(${fields.join(',')})';
    })
    .join(',');
