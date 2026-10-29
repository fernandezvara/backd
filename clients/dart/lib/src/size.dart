// Sizes: "10MiB", "1.5GB". The grammar is the server's (strutil.ParseSize in Go) and
// backd-js's; clients/testdata/size-vectors.json is run by all three.

/// Which units a size takes: [decimal] (B, KB..TB, powers of 1000), [binary] (B, KiB..TiB,
/// powers of 1024) or [both] (the default).
enum SizeFamily { both, decimal, binary }

final _units = <String, BigInt>{
  'B': BigInt.one,
  'KB': BigInt.from(1000),
  'MB': BigInt.from(1000000),
  'GB': BigInt.from(1000000000),
  'TB': BigInt.from(1000000000000),
  'KiB': BigInt.one << 10,
  'MiB': BigInt.one << 20,
  'GiB': BigInt.one << 30,
  'TiB': BigInt.one << 40,
};
const _names = {
  SizeFamily.both: 'B, KB, MB, GB, TB, KiB, MiB, GiB, TiB',
  SizeFamily.decimal: 'B, KB, MB, GB, TB',
  SizeFamily.binary: 'B, KiB, MiB, GiB, TiB',
};
final _pattern =
    RegExp(r'^([0-9]+)(?:\.([0-9]+))?(B|KB|MB|GB|TB|KiB|MiB|GiB|TiB)?$');

/// The bytes a size stands for. A unit follows the number directly and is
/// case-sensitive; a decimal is fine when the result is a whole number of bytes
/// (`0.1KiB` is not). [family] limits the units; [allowBytes] also accepts a bare
/// whole number of bytes (`"1048576"`). Throws a [FormatException] that names what is
/// accepted.
int parseSize(
  String value, {
  SizeFamily family = SizeFamily.both,
  bool allowBytes = false,
}) {
  final s = value.trim();
  final m = s.length <= 64 ? _pattern.firstMatch(s) : null;
  if (m == null || (m[3] == null && (!allowBytes || m[2] != null))) {
    throw FormatException(
        'invalid size "$value": want ${allowBytes ? 'a whole number of bytes or ' : ''}a number and a unit (${_names[family]}), such as ${family == SizeFamily.decimal ? '128MB' : '128MiB'}');
  }
  final unit = m[3] ?? 'B';
  final binary = unit.endsWith('iB');
  if ((family == SizeFamily.decimal && binary) ||
      (family == SizeFamily.binary && !binary && unit != 'B')) {
    throw FormatException(
        'invalid size "$value": $unit is not accepted here, use one of ${_names[family]}');
  }
  final frac = m[2] ?? '';
  final scale = BigInt.from(10).pow(frac.length);
  final total = BigInt.parse('${m[1]}$frac') * _units[unit]!;
  if (total % scale != BigInt.zero) {
    throw FormatException('invalid size "$value": not a whole number of bytes');
  }
  final bytes = total ~/ scale;
  if (bytes > BigInt.from(9007199254740991)) {
    throw FormatException('invalid size "$value": too large');
  }
  return bytes.toInt();
}

/// Bytes for people: the largest unit whose value is at least 1, up to TB or TiB, with at
/// most two decimals and no trailing zeros (`1.5 KiB`, `5 MiB`). [SizeFamily.both] prints
/// binary. It is for display and can lose digits.
String formatBytes(int n, {SizeFamily family = SizeFamily.both}) {
  final decimal = family == SizeFamily.decimal;
  final base = decimal ? 1000.0 : 1024.0;
  final units = decimal
      ? const ['B', 'KB', 'MB', 'GB', 'TB']
      : const ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  if (n < base) return '$n B';
  var v = n.toDouble();
  var i = 0;
  for (; i < units.length - 1; i++) {
    if ((v * 100).roundToDouble() / 100 < base) break;
    v /= base;
  }
  final r = (v * 100).roundToDouble() / 100;
  var text = r.toStringAsFixed(2);
  text = text.replaceFirst(RegExp(r'\.?0+$'), '');
  return '$text ${units[i]}';
}
