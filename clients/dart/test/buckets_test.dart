import 'package:backd/backd.dart';
import 'package:test/test.dart';

AggregateGroup bucket(String name, String key, Object count) =>
    AggregateGroup(key: {name: key}, metrics: {'count': count});

void main() {
  group('bucketKey', () {
    test('writes the keys the server writes', () {
      final d = DateTime.utc(2026, 3, 14, 23, 59);
      expect(bucketKey(d, BucketUnit.day), '2026-03-14');
      expect(bucketKey(d, BucketUnit.week), '2026-W11');
      expect(bucketKey(d, BucketUnit.month), '2026-03');
      expect(bucketKey(d, BucketUnit.year), '2026');
    });

    test('weeks follow ISO 8601 across the year boundary', () {
      expect(bucketKey(DateTime.utc(2026, 1, 1), BucketUnit.week), '2026-W01');
      expect(bucketKey(DateTime.utc(2027, 1, 1), BucketUnit.week), '2026-W53');
      expect(
          bucketKey(DateTime.utc(2024, 12, 30), BucketUnit.week), '2025-W01');
    });
  });

  group('fillBuckets', () {
    test('adds the empty months in order, with count 0 and the empty metrics',
        () {
      final out = fillBuckets(
        [
          bucket('paid_at:month', '2026-03', 4),
          bucket('paid_at:month', '2026-01', 2)
        ],
        field: 'paid_at',
        unit: BucketUnit.month,
        from: '2025-12',
        to: DateTime.utc(2026, 3, 5),
        empty: {'sum_total': '0'},
      );
      expect([for (final g in out) g.key['paid_at:month']],
          ['2025-12', '2026-01', '2026-02', '2026-03']);
      expect([for (final g in out) g.count], [0, 2, 0, 4]);
      expect(out[1].metrics.containsKey('sum_total'), isFalse);
      expect(out[0].metrics['sum_total'], '0');
    });

    test('weeks and days walk the calendar', () {
      final weeks = fillBuckets(
        [],
        field: 'd',
        unit: BucketUnit.week,
        from: '2026-W52',
        to: '2027-W02',
      );
      expect([for (final g in weeks) g.key['d:week']],
          ['2026-W52', '2026-W53', '2027-W01', '2027-W02']);
      final days = fillBuckets([],
          field: 'd',
          unit: BucketUnit.day,
          from: '2026-02-27',
          to: '2026-03-02');
      expect(days.length, 4);
    });

    test('a null group stays at the front', () {
      final out = fillBuckets(
        [
          AggregateGroup(key: {'d:year': null}, metrics: {'count': 1})
        ],
        field: 'd',
        unit: BucketUnit.year,
        from: '2025',
        to: '2026',
      );
      expect(out.first.key['d:year'], isNull);
      expect(out.length, 3);
    });

    test('refuses groups of another shape, a reversed range and a runaway one',
        () {
      expect(
        () => fillBuckets([bucket('x:day', '2026-01-01', 1)],
            field: 'd',
            unit: BucketUnit.day,
            from: '2026-01-01',
            to: '2026-01-02'),
        throwsArgumentError,
      );
      expect(
        () => fillBuckets([],
            field: 'd',
            unit: BucketUnit.day,
            from: '2026-02-01',
            to: '2026-01-01'),
        throwsArgumentError,
      );
      expect(
        () => fillBuckets([],
            field: 'd',
            unit: BucketUnit.day,
            from: '2000-01-01',
            to: '2026-01-01'),
        throwsRangeError,
      );
      expect(
        () => fillBuckets([],
            field: 'd', unit: BucketUnit.day, from: 'soon', to: '2026-01-01'),
        throwsArgumentError,
      );
    });
  });
}
