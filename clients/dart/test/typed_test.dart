import 'dart:convert';

import 'package:backd/backd.dart';
import 'package:test/test.dart';

import 'helpers.dart';

/// The kind of type `backd types --lang dart` generates.
final class Note {
  const Note({required this.id, required this.title, this.done = false});

  factory Note.fromJson(Map<String, Object?> json) => Note(
        id: json['id'] as String,
        title: json['title'] as String,
        done: json['done'] as bool? ?? false,
      );

  final String id;
  final String title;
  final bool done;
}

final class NoteInput {
  const NoteInput({required this.title, this.done});

  final String title;
  final bool? done;

  Map<String, Object?> toJson() =>
      {'title': title, if (done != null) 'done': done};
}

Map<String, Object?> noteJson(String id, String title,
        [bool done = false]) =>
    {
      'id': id,
      'title': title,
      'done': done,
      '_meta': {'created_at': '2024-01-01T00:00:00Z', 'updated_at': 'x'},
    };

TypedCollection<Note, NoteInput, NoteInput> notes(BackdClient<Object?> client) =>
    TypedCollection(
      client.db('main').collection('notes'),
      Note.fromJson,
      (i) => i.toJson(),
      (i) => i.toJson(),
    );

void main() {
  late FakeTransport transport;
  late BackdClient<Object?> client;
  late TypedCollection<Note, NoteInput, NoteInput> coll;

  setUp(() {
    transport = FakeTransport();
    client = testClient(transport, apiKey: 'k');
    coll = notes(client);
  });

  group('TypedCollection', () {
    test('list decodes each document', () async {
      transport.answer(respond(200, {
        'items': [noteJson('n1', 'a'), noteJson('n2', 'b', true)],
        'limit': 50,
        'skip': 0,
        'has_more': true,
        'next_cursor': 'cur',
      }));

      final page = await coll.list();

      expect(page.items, hasLength(2));
      expect(page.items[0].id, 'n1');
      expect(page.items[1].done, isTrue);
      expect(page.hasMore, isTrue);
      expect(page.nextCursor, 'cur');
    });

    test('iterate decodes each page', () async {
      transport.answer(respond(200, {
        'items': [noteJson('n1', 'a')],
        'limit': 1,
        'skip': 0,
        'has_more': true,
        'next_cursor': 'cur',
      }));
      transport.answer(respond(200, {
        'items': [noteJson('n2', 'b')],
        'limit': 1,
        'skip': 0,
        'has_more': false,
      }));

      final docs = await coll.iterate(limit: 1).toList();

      expect(docs.map((d) => d.title), ['a', 'b']);
      expect(transport.requests, hasLength(2));
      expect(transport.requests.last.url.queryParameters['after'], 'cur');
    });

    test('create encodes the input and decodes the answer', () async {
      transport.answer(respond(201, noteJson('n1', 'a')));

      final doc = await coll.create(const NoteInput(title: 'a'));

      expect(doc.id, 'n1');
      final body = jsonDecode(utf8.decode(transport.requests.single.body!));
      expect(body, {'title': 'a'});
    });

    test('replace encodes and decodes', () async {
      transport.answer(respond(200, noteJson('n1', 'b', true)));

      final doc = await coll.replace('n1', const NoteInput(title: 'b', done: true));

      expect(doc.done, isTrue);
      expect(transport.requests.single.method, 'PUT');
      expect(jsonDecode(utf8.decode(transport.requests.single.body!)),
          {'title': 'b', 'done': true});
    });

    test('get, patch and restore decode', () async {
      transport.answer(respond(200, noteJson('n1', 'a')));
      transport.answer(respond(200, noteJson('n1', 'a', true)));
      transport.answer(respond(200, noteJson('n1', 'a')));

      expect((await coll.get('n1')).title, 'a');
      expect((await coll.patch('n1', {'done': true})).done, isTrue);
      expect((await coll.restore('n1')).id, 'n1');
    });

    test('search decodes and delete stays void', () async {
      transport.answer(respond(200, {
        'items': [noteJson('n1', 'a')],
        'limit': 50,
        'skip': 0,
        'has_more': false,
      }));
      transport.answer(respond(204));

      expect((await coll.search('a')).items.single.id, 'n1');
      await coll.delete('n1');
      expect(transport.requests.last.method, 'DELETE');
    });

    test('scoping rides on the wrapped collection', () async {
      transport.answer(respond(200, {
        'items': [noteJson('n1', 'a')],
        'limit': 50,
        'skip': 0,
        'has_more': false,
      }));

      await coll.inTeam('t1').list();

      final q = transport.requests.single.url.queryParameters;
      expect(q['org'], isNull);
      expect(q['team'], 't1');
    });

    test('an untyped caller keeps Map documents', () async {
      transport.answer(respond(200, noteJson('n1', 'a')));

      final doc = await coll.raw.get('n1');

      expect(doc, isA<Map<String, Object?>>());
      expect(doc['title'], 'a');
    });
  });
}
