import 'dart:convert';

import 'package:backd/backd.dart';
import 'package:test/test.dart';

import 'helpers.dart';

Map<String, Object?> jobJson(String id, String status,
        [Map<String, Object?>? result, List<Map<String, Object?>>? steps]) =>
    {
      'id': id,
      'function': 'main/resize',
      'status': status,
      'created_at': '2026-10-01T00:00:00Z',
      if (result != null) 'result': result,
      if (steps != null) 'steps': steps,
    };

void main() {
  group('fn', () {
    test('a sync function returns its output', () async {
      final t = FakeTransport()
        ..answer(respond(200, {'ok': true}));
      final out = await testClient(t, apiKey: 'k')
          .db('main')
          .fn('resize', {'w': 10});
      final req = t.requests.single;
      expect(req.method, 'POST');
      expect(req.url.path, '/v1/shelf/main/_func/resize');
      expect(jsonDecode(utf8.decode(req.body!)), {'w': 10});
      expect(out, {'ok': true});
    });

    test('a 202 resolves to a Job; respondAsync forces it', () async {
      final t = FakeTransport()
        ..answer(respond(202, jobJson('job_1', 'queued')));
      final out = await testClient(t, apiKey: 'k')
          .db('main')
          .fn('resize', null, idempotencyKey: 'ik', respondAsync: true);
      expect(headerOf(t.requests.single, 'Prefer'), 'respond-async');
      expect(headerOf(t.requests.single, 'Idempotency-Key'), 'ik');
      final job = out as Job;
      expect(job.id, 'job_1');
      expect(job.function, 'main/resize');
    });
  });

  group('job', () {
    test('status() polls _jobs/{id}', () async {
      final t = FakeTransport()
        ..answer(respond(202, jobJson('job_1', 'queued')))
        ..answer(respond(200, jobJson('job_1', 'running')));
      final db = testClient(t, apiKey: 'k').db('main');
      final job = await db.fn('resize', null) as Job;
      expect(await job.status(), 'running');
      expect(t.requests[1].method, 'GET');
      expect(t.requests[1].url.path, '/v1/shelf/main/_jobs/job_1');
    });

    test('wait() polls to done and returns the output', () async {
      final t = FakeTransport()
        ..answer(respond(202, jobJson('job_1', 'queued')))
        ..answer(respond(200, jobJson('job_1', 'running', null, [
              {'n': 1, 'name': 'fetch', 'status': 'running'},
            ])))
        ..answer(respond(
            200, jobJson('job_1', 'done', {'status': 'ok', 'output': 42})));
      final db = testClient(t, apiKey: 'k').db('main');
      final job = await db.fn('resize', null) as Job;
      final seen = <int>[];
      final out = await job.wait(
          pollInterval: Duration.zero,
          onProgress: (steps, _) => seen.add(steps.length));
      expect(out, 42);
      // Each poll reports the steps that answer carried.
      expect(seen, [1, 0]);
      expect(job.steps, isEmpty);
    });

    test('wait() throws the error a sync call would have thrown', () async {
      final t = FakeTransport()
        ..answer(respond(202, jobJson('job_1', 'queued')))
        ..answer(respond(
            200,
            jobJson('job_1', 'done', {
              'status': 'function_error',
              'http_status': 422,
              'code': 'invalid_input',
              'message': 'width must be positive',
              'details': [
                {'path': 'w', 'reason': 'must be positive'},
              ],
            })));
      final db = testClient(t, apiKey: 'k').db('main');
      final job = await db.fn('resize', null) as Job;
      await expectLater(
        job.wait(pollInterval: Duration.zero),
        throwsA(isA<BackdException>()
            .having((e) => e.status, 'status', 422)
            .having((e) => e.code, 'code', 'invalid_input')
            .having((e) => e.details.single.path, 'detail', 'w')),
      );
    });

    test('wait() times out with JobTimeoutError while the job runs on',
        () async {
      final t = FakeTransport()
        ..answer(respond(202, jobJson('job_1', 'queued')))
        ..answer(respond(200, jobJson('job_1', 'running')))
        ..answer(respond(200, jobJson('job_1', 'done',
            {'status': 'ok', 'output': 'late'})));
      final db = testClient(t, apiKey: 'k').db('main');
      final job = await db.fn('resize', null) as Job;
      await expectLater(
        job.wait(pollInterval: Duration.zero, timeout: Duration.zero),
        throwsA(isA<JobTimeoutError>().having((e) => e.jobId, 'jobId', 'job_1')),
      );
      // The job itself is unaffected: wait() again still resolves.
      expect(await job.wait(pollInterval: Duration.zero), 'late');
    });

    test('a done job with no result is a StateError', () async {
      final t = FakeTransport()
        ..answer(respond(200, jobJson('job_1', 'done')));
      final job = Job(testClient(t, apiKey: 'k'), 'main', jobJson('job_1', 'done'));
      await expectLater(job.wait(pollInterval: Duration.zero),
          throwsA(isA<StateError>()));
    });
  });
}
