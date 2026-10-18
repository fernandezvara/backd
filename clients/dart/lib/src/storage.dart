import 'dart:async';
import 'dart:io';

/// Where the client keeps the session token. Methods may be async.
abstract interface class TokenStorage {
  /// The stored token, or null.
  FutureOr<String?> get();

  /// Stores [token].
  FutureOr<void> set(String token);

  /// Forgets the stored token.
  FutureOr<void> remove();
}

/// Keeps the token in memory: it is lost when the process ends. The default.
final class MemoryTokenStorage implements TokenStorage {
  String? _token;

  @override
  Future<String?> get() async => _token;

  @override
  Future<void> set(String token) async => _token = token;

  @override
  Future<void> remove() async => _token = null;
}

/// Keeps the token in a file of the app's choosing — what a standalone Dart
/// program wants to stay signed in between runs:
/// `FileTokenStorage('${Platform.environment['HOME']}/.backd.session')`.
/// The file holds the token and nothing else, in plain text: keep it out of
/// version control and out of a directory other users read.
final class FileTokenStorage implements TokenStorage {
  FileTokenStorage(this.path);

  /// The file's path.
  final String path;

  @override
  Future<String?> get() async {
    try {
      final text = await File(path).readAsString();
      return text.trim().isEmpty ? null : text.trim();
    } on PathNotFoundException {
      return null;
    } on FileSystemException {
      return null;
    }
  }

  @override
  Future<void> set(String token) =>
      File(path).writeAsString(token, flush: true);

  @override
  Future<void> remove() async {
    try {
      await File(path).delete();
    } on PathNotFoundException {
      // Already gone.
    }
  }
}
