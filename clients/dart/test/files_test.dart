import 'dart:convert';

import 'package:backd/backd.dart';
import 'package:crypto/crypto.dart';
import 'package:test/test.dart';

import 'helpers.dart';

Map<String, Object?> fileJson(String id) => {
      'id': id,
      'name': 'a.txt',
      'size': 3,
      'type': 'text/plain',
      'sha256': 'abc',
      'uploaded_at': '2026-10-01T00:00:00Z',
    };

void main() {
  group('files', () {
    test('list reads the document and picks the field', () async {
      final t = FakeTransport()
        ..answer(respond(200, {
          'id': 'doc1',
          'cover': fileJson('fl_1'),
          'attachments': [fileJson('fl_2'), fileJson('fl_3')],
        }));
      final col = testClient(t, apiKey: 'k').db('main').collection('docs');
      final one = await col.files('doc1', 'cover').list();
      expect(one.single['id'], 'fl_1');
    });

    test('get downloads the bytes; the single file is looked up first',
        () async {
      final t = FakeTransport()
        ..answer(respond(200, {'id': 'doc1', 'cover': fileJson('fl_1')}))
        ..answer(respond(200, 'hello bytes'));
      final col = testClient(t, apiKey: 'k').db('main').collection('docs');
      final content = await col.files('doc1', 'cover').get();
      expect(content.file?['id'], 'fl_1');
      expect(content.text(), 'hello bytes');
      expect(t.requests.last.url.path,
          '/v1/shelf/main/docs/doc1/_files/cover/fl_1');
    });

    test('get with a fileId skips the document read', () async {
      final t = FakeTransport()..answer(respond(200, 'raw'));
      final col = testClient(t, apiKey: 'k').db('main').collection('docs');
      await col.files('doc1', 'cover').get(fileId: 'fl_9');
      expect(t.requests, hasLength(1));
      expect(t.requests.single.url.path,
          '/v1/shelf/main/docs/doc1/_files/cover/fl_9');
    });

    test('get with no file in a single field is a NotFoundError', () async {
      final t = FakeTransport()..answer(respond(200, {'id': 'doc1'}));
      final col = testClient(t, apiKey: 'k').db('main').collection('docs');
      expect(() => col.files('doc1', 'cover').get(), throwsA(isA<NotFoundError>()));
    });

    test('get with several files and no fileId asks which', () async {
      final t = FakeTransport()
        ..answer(respond(
            200, {'id': 'doc1', 'att': [fileJson('fl_1'), fileJson('fl_2')]}));
      final col = testClient(t, apiKey: 'k').db('main').collection('docs');
      expect(() => col.files('doc1', 'att').get(), throwsArgumentError);
    });

    test('put sends the bytes with name and If-Match', () async {
      final t = FakeTransport()
        ..answer(respond(200, {'id': 'doc1', 'cover': fileJson('fl_4')}));
      final col = testClient(t, apiKey: 'k').db('main').collection('docs');
      final doc = await col
          .files('doc1', 'cover')
          .put('payload', name: 'note.txt', type: 'text/plain', ifMatch: 4);
      final req = t.requests.single;
      expect(req.method, 'POST');
      expect(req.url.path, '/v1/shelf/main/docs/doc1/_files/cover');
      expect(req.url.queryParameters['name'], 'note.txt');
      expect(headerOf(req, 'Content-Type'), 'text/plain');
      expect(headerOf(req, 'If-Match'), '"4"');
      expect(utf8.decode(req.body!), 'payload');
      expect(doc['id'], 'doc1');
    });

    test('delete one file, or the whole field', () async {
      final t = FakeTransport()
        ..answer(respond(200, {'id': 'doc1'}))
        ..answer(respond(200, {'id': 'doc1'}));
      final col = testClient(t, apiKey: 'k').db('main').collection('docs');
      await col.files('doc1', 'att').delete(fileId: 'fl_1', ifMatch: 2);
      expect(t.requests[0].url.path,
          '/v1/shelf/main/docs/doc1/_files/att/fl_1');
      expect(headerOf(t.requests[0], 'If-Match'), '"2"');
      await col.files('doc1', 'att').delete();
      expect(t.requests[1].url.path, '/v1/shelf/main/docs/doc1/_files/att');
    });

    test('link returns a credential-free URL', () async {
      final t = FakeTransport()
        ..answer(respond(
            200, {'url': 'https://cdn/x', 'expires_at': '2026-10-01T01:00:00Z'}));
      final col = testClient(t, apiKey: 'k').db('main').collection('docs');
      final l =
          await col.files('doc1', 'cover').link(fileId: 'fl_1', version: 'thumb');
      expect(l.url, 'https://cdn/x');
      expect(t.requests.single.url.queryParameters['link'], 'json');
      expect(t.requests.single.url.queryParameters['version'], 'thumb');
    });

    test('version regenerate/generate/delete hit the version routes',
        () async {
      final t = FakeTransport()
        ..answer(respond(200, {'status': 'pending'}))
        ..answer(respond(200, {'status': 'ready', 'id': 'fv_1'}))
        ..answer(respond(200, {'status': 'empty'}));
      final col = testClient(t, apiKey: 'k').db('main').collection('docs');
      final v = col.files('doc1', 'cover').version('thumb', fileId: 'fl_1');
      expect((await v.regenerate()).status, 'pending');
      expect(t.requests[0].method, 'POST');
      expect(t.requests[0].url.path,
          '/v1/shelf/main/docs/doc1/_files/cover/fl_1/versions/thumb');
      expect((await v.generate({'max_width': 64})).status, 'ready');
      expect(jsonDecode(utf8.decode(t.requests[1].body!)),
          {'max_width': 64});
      expect((await v.delete()).status, 'empty');
      expect(t.requests[2].method, 'DELETE');
    });
  });

  group('uploadFile', () {
    test('proxy: field lookup once, then the bytes through backd', () async {
      final t = FakeTransport()
        ..answer(respond(200, {
          'name': 'cover',
          'upload': 'proxy',
          'download': 'presigned',
          'multiple': false,
          'max_files': 1,
          'max_size': 1000,
          'types': <String>[],
        }))
        ..answer(respond(200, {'id': 'doc1', 'cover': fileJson('fl_5')}));
      final col = testClient(t, apiKey: 'k').db('main').collection('docs');
      final doc = await col.uploadFile('doc1', 'cover', 'data',
          name: 'd.txt');
      expect(t.requests[0].url.path, '/v1/shelf/main/docs/_files/cover');
      expect(t.requests[1].url.path,
          '/v1/shelf/main/docs/doc1/_files/cover');
      expect(doc['id'], 'doc1');
    });

    test('direct: declare, PUT to the bucket, complete', () async {
      final bytes = utf8.encode('direct-data');
      final digest = sha256.convert(bytes).toString();
      final t = FakeTransport()
        // Field lookup.
        ..answer(respond(200, {
          'name': 'cover',
          'upload': 'direct',
          'download': 'presigned',
          'multiple': false,
          'max_files': 1,
          'max_size': 100000,
          'types': <String>[],
        }))
        // Declare → signed URL.
        ..answer(respond(200, {
          'upload_id': 'up_1',
          'upload_token': 'tok',
          'url': 'https://bucket.example/signed',
          'headers': {'X-Custom': 'yes'},
          'expires_at': '2026-10-01T02:00:00Z',
        }))
        // The bucket PUT (absolute URL, no auth).
        ..answer(respond(200))
        // Complete.
        ..answer(respond(200, {'id': 'doc1', 'cover': fileJson('fl_6')}));
      final col = testClient(t, apiKey: 'k').db('main').collection('docs');
      final doc = await col.uploadFile('doc1', 'cover', bytes, name: 'd.bin');
      expect(t.requests, hasLength(4));
      final declare = jsonDecode(utf8.decode(t.requests[1].body!));
      expect(declare['sha256'], digest);
      expect(declare['name'], 'd.bin');
      final put = t.requests[2];
      expect('${put.url}', 'https://bucket.example/signed');
      expect(headerOf(put, 'X-Custom'), 'yes');
      expect(headerOf(put, 'Authorization'), isNull);
      final complete = t.requests[3];
      expect(complete.url.path,
          '/v1/shelf/main/docs/_files/cover/uploads/up_1/complete');
      expect(
          jsonDecode(utf8.decode(complete.body!)), {'upload_token': 'tok'});
      expect(doc['id'], 'doc1');
    });
  });

  group('prepareUpload', () {
    test('proxy: uploads to the collection and gives back the ref', () async {
      final t = FakeTransport()
        ..answer(respond(200, {
          'name': 'cover',
          'upload': 'proxy',
          'download': 'presigned',
          'multiple': false,
          'max_files': 1,
          'max_size': 1000,
          'types': <String>[],
        }))
        ..answer(respond(200, {
          'upload_id': 'up_9',
          'upload_token': 'tk',
          'expires_at': '2026-10-01T03:00:00Z',
          'file': {'name': 'p.txt', 'size': 1, 'type': 'text/plain', 'sha256': 'h'},
        }));
      final col = testClient(t, apiKey: 'k').db('main').collection('docs');
      final up = await col.prepareUpload('cover', 'x', name: 'p.txt');
      expect(t.requests[1].url.path,
          '/v1/shelf/main/docs/_files/cover/uploads');
      expect(up.ref, {'upload': 'up_9', 'token': 'tk'});
      expect(up.file['name'], 'p.txt');
    });
  });

  group('fileUrl', () {
    test('returns the link; null for an unavailable version', () async {
      final t = FakeTransport()
        ..answer(respond(200, {'url': 'u', 'expires_at': 'e'}))
        ..answer(respond(422, {
          'error': {'code': 'version_unavailable', 'message': 'not ready'},
        }));
      final col = testClient(t, apiKey: 'k').db('main').collection('docs');
      final l = await col.fileUrl('doc1', 'cover', 'fl_1');
      expect(l?.url, 'u');
      expect(await col.fileUrl('doc1', 'cover', 'fl_1', version: 'thumb'),
          isNull);
    });
  });

  group('downloadFile and deleteFile', () {
    test('downloadFile asks for a fresh link and never returns null', () async {
      final t = FakeTransport()
        ..answer(respond(200, {'url': 'https://cdn/y', 'expires_at': 'e'}));
      final col = testClient(t, apiKey: 'k').db('main').collection('docs');
      final l = await col.downloadFile('doc1', 'cover', 'fl_1');
      expect(l.url, 'https://cdn/y');
      expect(t.requests.single.url.queryParameters['link'], 'json');
    });

    test('deleteFile removes one file under If-Match', () async {
      final t = FakeTransport()
        ..answer(respond(200, {'id': 'doc1', 'attachments': <Object?>[]}));
      final col = testClient(t, apiKey: 'k').db('main').collection('docs');
      final doc = await col.deleteFile('doc1', 'attachments', 'fl_2', ifMatch: 3);
      expect(doc['id'], 'doc1');
      expect(t.requests.single.method, 'DELETE');
      expect(t.requests.single.url.path,
          '/v1/shelf/main/docs/doc1/_files/attachments/fl_2');
      expect(t.requests.single.headers['If-Match'], '"3"');
    });

    test('deleteFile needs the file id', () {
      final col =
          testClient(FakeTransport(), apiKey: 'k').db('main').collection('docs');
      expect(() => col.deleteFile('doc1', 'attachments', ''), throwsArgumentError);
    });
  });

  group('helpers', () {
    test('partSizeFor stays within bounds and part multiples', () {
      const mib = 1024 * 1024;
      expect(partSizeFor(1), 64 * mib);
      expect(partSizeFor(64 * mib), 64 * mib);
      // 65 GiB needs parts of 68 MiB+ → rounded to a 16 MiB multiple.
      expect(partSizeFor(65 * 1024 * mib) % (16 * mib), 0);
      expect(partSizeFor(65 * 1024 * mib) * 1000 >= 65 * 1024 * mib, isTrue);
    });

    test('versionStatus reads the state a document holds', () {
      final file = {
        'versions': {
          'thumb': {'status': 'ready', 'width': 10, 'height': 20},
          'big': {'status': 'skipped', 'reason': 'not_an_image'},
        },
      };
      expect(versionStatus(file, 'thumb').ready, isTrue);
      expect(versionStatus(file, 'thumb').width, 10);
      expect(versionStatus(file, 'big').status, 'skipped');
      expect(versionStatus(file, 'big').reason, 'not_an_image');
      expect(versionStatus(file, 'none').status, 'undeclared');
    });

    final publicFile = {
      ...fileJson('fl_pub'),
      'public': true,
      'key_name': 'Me.png',
      'url': 'https://cdn.test/prod/shelf/main/docs/fl_pub/Me.png',
      'versions': {
        'thumb': {
          'status': 'ready',
          'url': 'https://cdn.test/prod/shelf/main/docs/fl_pub/thumb/Me.png'
        },
        'big': {'status': 'pending'},
      },
    };

    test('publicUrl is the address a public file carries', () {
      expect(publicUrl(publicFile),
          'https://cdn.test/prod/shelf/main/docs/fl_pub/Me.png');
      expect(publicUrl(publicFile, version: 'thumb'),
          'https://cdn.test/prod/shelf/main/docs/fl_pub/thumb/Me.png');
      expect(publicUrl(publicFile, version: 'big'), isNull); // not made yet
      expect(publicUrl(publicFile, version: 'nope'), isNull);
      expect(publicUrl(fileJson('fl_1')), isNull); // not a public file
      expect(
          publicUrl({...fileJson('fl_1'), 'url': 'https://signed/x'}), isNull);
      expect(publicUrl(null), isNull);
    });

    test('fileUrl answers a public file from its details, without a request',
        () async {
      final t = FakeTransport();
      final col = testClient(t, apiKey: 'k').db('main').collection('docs');
      final link =
          await col.fileUrl('doc1', 'cover', 'fl_pub', file: publicFile);
      expect(link!.url, publicFile['url']);
      expect(link.expiresAt, isNull);
      final thumb = await col.fileUrl('doc1', 'cover', 'fl_pub',
          file: publicFile, version: 'thumb');
      expect(thumb!.url, endsWith('/thumb/Me.png'));
      expect(
          await col.fileUrl('doc1', 'cover', 'fl_pub',
              file: publicFile, version: 'big'),
          isNull);
      expect(t.requests, isEmpty);
    });

    test('fileUrl still asks the server for anything else', () async {
      final t = FakeTransport()
        ..answer(respond(200,
            {'url': 'https://s3/x', 'expires_at': '2026-10-06T10:05:00.000Z'}));
      final col = testClient(t, apiKey: 'k').db('main').collection('docs');
      final link =
          await col.fileUrl('doc1', 'cover', 'fl_1', file: fileJson('fl_1'));
      expect(link!.expiresAt, '2026-10-06T10:05:00.000Z');
      expect(t.requests, hasLength(1));
    });
  });
}
