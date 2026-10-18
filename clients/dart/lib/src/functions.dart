import 'dart:async';

import 'client.dart';
import 'errors.dart';

/// `job.wait()` gave up before the job finished — the job itself is
/// unaffected and still running; call [Job.wait] again, or poll with
/// [Job.status], whenever you like.
final class JobTimeoutError extends Error {
  JobTimeoutError(this.jobId);

  final String jobId;

  @override
  String toString() => 'job $jobId did not finish within the given timeout';
}

/// A handle to an `async` function's job: `db.fn(name, input)` returns one
/// instead of the output directly when the function is `async`. Poll with
/// [status], or use [wait] to get the output the same way a `sync` call's
/// return value works.
final class Job {
  Job(this.client, this.database, this.data);

  final BackdClient<Object?> client;
  final String database;

  /// The job's data as of the last [status] or [wait] call, or when it was
  /// created.
  Map<String, Object?> data;

  String get id => '${data['id']}';

  /// The function this job runs, as `<database>/<name>`.
  String get function => '${data['function']}';

  /// The steps the function reported with `ctx.step()`, as of the last
  /// [status] or [wait] poll (empty before one, or when it reports none):
  /// the last is the current step.
  List<Map<String, Object?>> get steps => [
        for (final s in data['steps'] as List? ?? const [])
          if (s is Map) Map<String, Object?>.from(s),
      ];

  /// Polls the job's current status: `queued`, `running` or `done`.
  Future<String> status({RequestOptions? options}) async {
    data = await _fetch(options);
    return '${data['status']}';
  }

  /// Polls until the job is done, then returns its output — the same value
  /// a `sync` call would have returned — or throws a [BackdException]
  /// matching what a `sync` call would have thrown for the same outcome
  /// (the function's own `code`, when it threw `ctx.error(...)`).
  ///
  /// [pollInterval] defaults to 500 ms. Without [timeout] it waits
  /// indefinitely. [onProgress] is called after every poll with the steps
  /// reported so far.
  Future<Object?> wait({
    Duration pollInterval = const Duration(milliseconds: 500),
    Duration? timeout,
    void Function(List<Map<String, Object?>> steps, Job job)? onProgress,
    RequestOptions? options,
  }) async {
    final deadline =
        timeout == null ? null : DateTime.now().add(timeout);
    for (;;) {
      data = await _fetch(options);
      onProgress?.call(steps, this);
      if (data['status'] == 'done') {
        return _outputOrThrow(data['result']);
      }
      if (deadline != null && !DateTime.now().isBefore(deadline)) {
        throw JobTimeoutError(id);
      }
      await Future<void>.delayed(pollInterval);
    }
  }

  Future<Map<String, Object?>> _fetch(RequestOptions? options) async {
    final res = await client.request(RequestInit(
      method: 'GET',
      path: [database, '_jobs', id],
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }

  Object? _outputOrThrow(Object? result) {
    if (result is! Map) {
      throw StateError('job $id is done but has no result');
    }
    if (result['status'] == 'ok') return result['output'];
    throw errorForStatus(
      (result['http_status'] as num?)?.toInt() ?? 500,
      code: result['code'] as String? ?? 'function_failed',
      message: result['message'] as String? ?? 'the function failed',
      details: [
        for (final d in result['details'] as List? ?? const [])
          ErrorDetail.fromJson(d),
      ],
    );
  }
}
