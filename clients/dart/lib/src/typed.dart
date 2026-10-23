import 'client.dart' show RequestOptions;
import 'data.dart';
import 'files.dart' as fileio;
import 'transport.dart' show UploadProgress;

/// A typed page: [Page] with decoded items.
final class TypedPage<D> {
  const TypedPage({
    required this.items,
    required this.limit,
    required this.skip,
    required this.hasMore,
    this.nextCursor,
    this.total,
  });

  final List<D> items;
  final int limit;
  final int skip;
  final bool hasMore;
  final String? nextCursor;
  final int? total;
}

/// A collection bound to its generated types, from `backd types --lang dart`:
/// reads answer `D` and writes take `I` (`C` — `I` plus the id — in a
/// `custom_id` collection). It wraps the underlying [Collection] and keeps the
/// same signatures; everything the generated types can't express (`where`,
/// `orderBy`, `patch`) still takes the untyped shapes.
final class TypedCollection<D, I, C> {
  const TypedCollection(this.raw, this._decode, this._encode, this._encodeCreate);

  /// The underlying untyped collection.
  final Collection raw;

  final D Function(Map<String, Object?>) _decode;
  final Map<String, Object?> Function(I) _encode;
  final Map<String, Object?> Function(C) _encodeCreate;

  TypedCollection<D, I, C> inOrg(String org) =>
      TypedCollection(raw.inOrg(org), _decode, _encode, _encodeCreate);

  TypedCollection<D, I, C> inTeam(String team) =>
      TypedCollection(raw.inTeam(team), _decode, _encode, _encodeCreate);

  Future<TypedPage<D>> list({
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
    final page = await raw.list(
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
      options: options,
    );
    return TypedPage(
      items: [for (final d in page.items) _decode(d)],
      limit: page.limit,
      skip: page.skip,
      hasMore: page.hasMore,
      nextCursor: page.nextCursor,
      total: page.total,
    );
  }

  Stream<D> iterate({
    Object? where,
    Object? orderBy,
    int? limit,
    String? after,
    bool? fileLinks,
    String? language,
    String? expand,
    DeletedDocs? deleted,
    RequestOptions? options,
  }) =>
      raw
          .iterate(
            where: where,
            orderBy: orderBy,
            limit: limit,
            after: after,
            fileLinks: fileLinks,
            language: language,
            expand: expand,
            deleted: deleted,
            options: options,
          )
          .map(_decode);

  Future<AggregateResult> aggregate({
    Object? where,
    Object? groupBy,
    Object? metrics,
    String? tz,
    Object? orderBy,
    int? limit,
    String? language,
    RequestOptions? options,
  }) =>
      raw.aggregate(
        where: where,
        groupBy: groupBy,
        metrics: metrics,
        tz: tz,
        orderBy: orderBy,
        limit: limit,
        language: language,
        options: options,
      );

  Future<TypedPage<D>> search(
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
  }) async {
    final page = await raw.search(
      text,
      where: where,
      limit: limit,
      skip: skip,
      count: count,
      fileLinks: fileLinks,
      language: language,
      expand: expand,
      deleted: deleted,
      options: options,
    );
    return TypedPage(
      items: [for (final d in page.items) _decode(d)],
      limit: page.limit,
      skip: page.skip,
      hasMore: page.hasMore,
      nextCursor: page.nextCursor,
      total: page.total,
    );
  }

  fileio.Files files(String id, String field) => raw.files(id, field);

  Future<fileio.FileField> field(String field, {RequestOptions? options}) =>
      raw.field(field, options: options);

  Future<D> uploadFile(
    String id,
    String field,
    Object file, {
    String? name,
    String? type,
    Object? ifMatch,
    void Function(UploadProgress)? onProgress,
    RequestOptions? options,
  }) async =>
      _decode(await raw.uploadFile(id, field, file,
          name: name,
          type: type,
          ifMatch: ifMatch,
          onProgress: onProgress,
          options: options));

  Future<fileio.PreparedUpload> prepareUpload(
    String field,
    Object file, {
    String? name,
    String? type,
    void Function(UploadProgress)? onProgress,
    RequestOptions? options,
  }) =>
      raw.prepareUpload(field, file,
          name: name, type: type, onProgress: onProgress, options: options);

  Future<fileio.FileLink?> fileUrl(
    String id,
    String field,
    String fileId, {
    String? version,
    RequestOptions? options,
  }) =>
      raw.fileUrl(id, field, fileId, version: version, options: options);

  Future<fileio.FileLink> downloadFile(
    String id,
    String field,
    String fileId, {
    RequestOptions? options,
  }) =>
      raw.downloadFile(id, field, fileId, options: options);

  Future<D> deleteFile(
    String id,
    String field,
    String fileId, {
    Object? ifMatch,
    RequestOptions? options,
  }) async =>
      _decode(await raw.deleteFile(id, field, fileId,
          ifMatch: ifMatch, options: options));

  Future<D> get(
    String id, {
    DeletedDocs? deleted,
    bool? fileLinks,
    String? expand,
    RequestOptions? options,
  }) async =>
      _decode(await raw.get(id,
          deleted: deleted,
          fileLinks: fileLinks,
          expand: expand,
          options: options));

  Future<D> create(
    C doc, {
    String? idempotencyKey,
    String? org,
    String? team,
    RequestOptions? options,
  }) async =>
      _decode(await raw.create(_encodeCreate(doc),
          idempotencyKey: idempotencyKey, org: org, team: team, options: options));

  Future<D> replace(
    String id,
    I doc, {
    Object? ifMatch,
    RequestOptions? options,
  }) async =>
      _decode(await raw.replace(id, _encode(doc), ifMatch: ifMatch, options: options));

  Future<D> patch(
    String id,
    Map<String, Object?> patch, {
    Object? ifMatch,
    RequestOptions? options,
  }) async =>
      _decode(await raw.patch(id, patch, ifMatch: ifMatch, options: options));

  Future<void> delete(
    String id, {
    bool? purge,
    Object? ifMatch,
    RequestOptions? options,
  }) =>
      raw.delete(id, purge: purge, ifMatch: ifMatch, options: options);

  Future<D> restore(
    String id, {
    Object? ifMatch,
    RequestOptions? options,
  }) async =>
      _decode(await raw.restore(id, ifMatch: ifMatch, options: options));
}
