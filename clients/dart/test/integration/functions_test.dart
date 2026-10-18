// Functions against a real backd + executor: sync calls, async jobs and a
// function's own error. Needs `make dart-integration`.
@TestOn('vm')
library;

import 'package:backd/backd.dart';
import 'package:test/test.dart';

import 'env.dart' as env;

void main() {
  group('functions', skip: env.skip, () {
    test('echo doubles, echo_async waits, a thrown error arrives', () async {
      final u = await env.user('fn');
      final db = u.client.db('app');
      addTearDown(() async => u.client.close());

      final out = await db.fn('echo', {'n': 21});
      expect((out as Map)['doubled'], 42);

      final job = await db.fn('echo_async', {'n': 7}, respondAsync: true);
      expect(job, isA<Job>());
      final done = await (job as Job)
          .wait(timeout: const Duration(seconds: 30));
      expect((done as Map)['doubled'], 14);

      // The function's own error is a client error of the right kind.
      try {
        await db.fn('echo', {'fail': true});
        fail('expected a thrown error');
      } on BackdException catch (e) {
        expect(e.code, 'out_of_stock');
        expect(e.status, 409);
      }
    });
  });
}
