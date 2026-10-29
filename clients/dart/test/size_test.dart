import 'dart:convert';
import 'dart:io';

import 'package:backd/backd.dart';
import 'package:test/test.dart';

SizeFamily familyOf(String s) => SizeFamily.values.byName(s);

void main() {
  // Shared with the Go and JavaScript tests: one grammar in three languages.
  final vectors =
      jsonDecode(File('../testdata/size-vectors.json').readAsStringSync())
          as Map<String, dynamic>;

  test('parseSize follows the shared vectors', () {
    for (final c in vectors['parse'] as List) {
      final family = familyOf(c['family'] as String);
      final allow = c['allowBytes'] as bool;
      final label = '${jsonEncode(c['in'])} ($family, $allow)';
      if (c['error'] == true) {
        expect(
            () =>
                parseSize(c['in'] as String, family: family, allowBytes: allow),
            throwsFormatException,
            reason: label);
      } else {
        expect(parseSize(c['in'] as String, family: family, allowBytes: allow),
            c['bytes'],
            reason: label);
      }
    }
  });

  test('formatBytes follows the shared vectors', () {
    for (final c in vectors['format'] as List) {
      expect(
          formatBytes(c['bytes'] as int,
              family: familyOf(c['family'] as String)),
          c['out'],
          reason: '${c['bytes']} ${c['family']}');
    }
  });

  test('errors name what the field takes', () {
    expect(
        () => parseSize('2MB', family: SizeFamily.binary),
        throwsA(isA<FormatException>().having(
            (e) => e.message, 'message', contains('KiB, MiB, GiB, TiB'))));
    expect(
        () => parseSize('0.1KiB'),
        throwsA(isA<FormatException>().having(
            (e) => e.message, 'message', contains('whole number of bytes'))));
  });
}
