import 'data.dart' show AggregateGroup;

/// The size of a date bucket in an aggregation (`groupBy: 'paid_at:month'`).
enum BucketUnit { day, week, month, year }

final _keyShape = {
  BucketUnit.day: RegExp(r'^\d{4}-\d{2}-\d{2}$'),
  BucketUnit.week: RegExp(r'^\d{4}-W\d{2}$'),
  BucketUnit.month: RegExp(r'^\d{4}-\d{2}$'),
  BucketUnit.year: RegExp(r'^\d{4}$'),
};

String _pad(int n, [int width = 2]) => n.toString().padLeft(width, '0');

/// The ISO 8601 week-year and week of a date, in UTC (weeks start on Monday;
/// week 1 holds January 4th).
({int year, int week}) _isoWeek(DateTime d) {
  final u = DateTime.utc(d.year, d.month, d.day);
  // The Thursday of this week decides the year.
  final thursday = u.add(Duration(days: 4 - u.weekday));
  final yearStart = DateTime.utc(thursday.year);
  final dayOfYear = thursday.difference(yearStart).inDays + 1;
  return (year: thursday.year, week: (dayOfYear / 7).ceil());
}

/// The bucket key of a date as the server writes it, seen in UTC:
/// `2026-03-14`, `2026-W11`, `2026-03`, `2026`.
String bucketKey(DateTime date, BucketUnit unit) {
  final d = date.toUtc();
  switch (unit) {
    case BucketUnit.day:
      return '${_pad(d.year, 4)}-${_pad(d.month)}-${_pad(d.day)}';
    case BucketUnit.week:
      final w = _isoWeek(d);
      return '${_pad(w.year, 4)}-W${_pad(w.week)}';
    case BucketUnit.month:
      return '${_pad(d.year, 4)}-${_pad(d.month)}';
    case BucketUnit.year:
      return _pad(d.year, 4);
  }
}

DateTime _keyStart(String key, BucketUnit unit) {
  switch (unit) {
    case BucketUnit.day:
      return DateTime.parse('${key}T00:00:00Z');
    case BucketUnit.month:
      return DateTime.parse('$key-01T00:00:00Z');
    case BucketUnit.year:
      return DateTime.parse('$key-01-01T00:00:00Z');
    case BucketUnit.week:
      final parts = key.split('-W');
      final year = int.parse(parts[0]);
      final week = int.parse(parts[1]);
      final jan4 = DateTime.utc(year, 1, 4);
      final monday = jan4.subtract(Duration(days: jan4.weekday - 1));
      return monday.add(Duration(days: (week - 1) * 7));
  }
}

DateTime _next(BucketUnit unit, DateTime d) {
  switch (unit) {
    case BucketUnit.day:
      return DateTime.utc(d.year, d.month, d.day + 1);
    case BucketUnit.week:
      return DateTime.utc(d.year, d.month, d.day + 7);
    case BucketUnit.month:
      return DateTime.utc(d.year, d.month + 1);
    case BucketUnit.year:
      return DateTime.utc(d.year + 1);
  }
}

String _toKey(Object v, BucketUnit unit, String what) {
  if (v is String && _keyShape[unit]!.hasMatch(v)) return v;
  final d = v is DateTime ? v : (v is String ? DateTime.tryParse(v) : null);
  if (d == null) {
    throw ArgumentError.value(v, what,
        'not a date or a ${unit.name} key like ${bucketKey(DateTime.utc(1970), unit)}');
  }
  return bucketKey(d, unit);
}

/// Fills the buckets an aggregation left out, so a chart has a point for
/// every day, week, month or year between [from] and [to] (inclusive). The
/// server doesn't return empty buckets; this adds them to [groups] grouped by
/// one date field and nothing else (`groupBy: ['paid_at:month']`). A bucket
/// with no group gets `count: 0` and the metrics of [empty] (for sums,
/// `{'sum_total': '0'}`).
///
/// [from] and [to] are bucket keys in the zone of the aggregation
/// (`'2026-01'`), or [DateTime]s, which name the bucket they fall in as seen
/// in UTC. The groups come back in key order; a `null` group (documents with
/// no date) is kept at the front.
List<AggregateGroup> fillBuckets(
  List<AggregateGroup> groups, {
  required String field,
  required BucketUnit unit,
  required Object from,
  required Object to,
  Map<String, Object?> empty = const {},
}) {
  final name = '$field:${unit.name}';
  for (final g in groups) {
    if (g.key.length != 1 || !g.key.containsKey(name)) {
      throw ArgumentError(
          'fillBuckets needs groups grouped only by $name, got ${g.key.keys.toList()}');
    }
  }
  final first = _toKey(from, unit, 'from');
  final last = _toKey(to, unit, 'to');
  if (first.compareTo(last) > 0) {
    throw ArgumentError('from ($first) is after to ($last)');
  }
  final byKey = {for (final g in groups) g.key[name]: g};
  final out = <AggregateGroup>[];
  if (byKey.containsKey(null)) out.add(byKey[null]!);
  const limit = 5000;
  var cursor = _keyStart(first, unit);
  for (var i = 0;; i++) {
    final key = bucketKey(cursor, unit);
    if (key.compareTo(last) > 0) break;
    if (i >= limit) {
      throw RangeError(
          'more than $limit buckets between $first and $last: use a larger unit');
    }
    out.add(byKey[key] ??
        AggregateGroup(key: {name: key}, metrics: {'count': 0, ...empty}));
    cursor = _next(unit, cursor);
  }
  return out;
}
