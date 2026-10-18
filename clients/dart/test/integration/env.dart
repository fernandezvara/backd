// Shared helpers for the integration tests (run with `make dart-integration`:
// they expect the realms and keys that clients/dart/test/integration/run.sh
// prepares). Without BACKD_URL set they are skipped.
import 'dart:io';

import 'package:backd/backd.dart';

final url = Platform.environment['BACKD_URL'] ?? '';
final apiKey = Platform.environment['BACKD_API_KEY'] ?? '';
final filesApiKey = Platform.environment['BACKD_FILES_API_KEY'] ?? '';

/// Set when there is no server to test against.
final skip = url.isEmpty
    ? 'BACKD_URL not set; run `make dart-integration`'
    : null;

final _run = DateTime.now().millisecondsSinceEpoch.toRadixString(36);
var _n = 0;

/// A unique email for this run.
String email(String name) => '$name.$_run.${_n++}@example.com';

const password = 'dev-p4ssw0rd!';

/// A client for the itest realm, an admin API key on it; sessions in memory.
BackdClient<Object?> client() => BackdClient<Object?>(
      url: url,
      realm: 'itest',
      apiKey: apiKey.isEmpty ? null : apiKey,
    );

/// A client that signed up a new user of the itest realm.
Future<({BackdClient<Object?> client, String email})> user(String name) async {
  final c = BackdClient<Object?>(url: url, realm: 'itest');
  final address = email(name);
  await c.auth.signup(address, password);
  return (client: c, email: address);
}

/// A signed-up user of the files realm (its rules require an owner).
Future<({BackdClient<Object?> client, String email})> filesUser(
    String name) async {
  final c = BackdClient<Object?>(url: url, realm: 'files');
  final address = email(name);
  await c.auth.signup(address, password);
  return (client: c, email: address);
}
