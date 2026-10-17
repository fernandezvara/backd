// Files against a real backd + MinIO: a document upload through backd, a
// direct upload to the bucket, the bytes back, a signed link. Needs
// `make dart-integration`.
@TestOn('vm')
library;

import 'dart:convert';

import 'package:test/test.dart';

import 'env.dart' as env;

void main() {
  group('files', skip: env.skip, () {
    test('upload through backd, bytes back, signed link', () async {
      final u = await env.filesUser('upload');
      final docs = u.client.db('app').collection('docs');
      addTearDown(() async => u.client.close());

      final bytes = utf8.encode('attachment contents');
      final doc = await docs.create({'title': 'with-file'});
      final updated = await docs.uploadFile(
          doc['id'] as String, 'attachment', bytes,
          name: 'note.txt');

      // The document holds the file's details.
      final details = (updated['attachment'] as Map).cast<String, Object?>();
      expect(details['name'], 'note.txt');
      expect(details['size'], bytes.length);

      // The bytes come back.
      final got = await docs
          .files(doc['id'] as String, 'attachment')
          .get(fileId: details['id'] as String);
      expect(utf8.decode(got.bytes), 'attachment contents');

      // A signed link for the host's MinIO.
      final link = await docs.fileUrl(
          doc['id'] as String, 'attachment', details['id'] as String);
      expect(link, isNotNull);
      expect(link!.url, isNotEmpty);
    });

    test('direct upload goes straight to the bucket', () async {
      final u = await env.filesUser('direct');
      final docs = u.client.db('app').collection('docs');
      addTearDown(() async => u.client.close());

      // The `video` field is `upload: direct`: the client declares, sends to
      // the signed link, completes.
      final doc = await docs.create({'title': 'direct'});
      final updated = await docs.uploadFile(
          doc['id'] as String, 'video', utf8.encode('plain text video'),
          name: 'clip.txt', type: 'text/plain');
      final details = (updated['video'] as Map).cast<String, Object?>();
      expect(details['id'], isNotNull);

      final got = await docs
          .files(doc['id'] as String, 'video')
          .get(fileId: details['id'] as String);
      expect(utf8.decode(got.bytes), 'plain text video');
    });
  });
}
