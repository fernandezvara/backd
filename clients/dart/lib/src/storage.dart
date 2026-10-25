import 'dart:async';
import 'dart:io';

/// The slot name of the note kept beside an organization's token: when it ends,
/// which organization it is, what it is called. For [Orgs.signedIn].
const orgNotePrefix = 'meta.';

/// Where the client keeps the session token. Methods may be async.
///
/// A storage that can also hold one token per organization ([slot]) lets one
/// app be signed in to several organizations at once, with
/// `client.org(id)`. The built-in storages can.
abstract interface class TokenStorage {
  /// The stored token, or null.
  FutureOr<String?> get();

  /// Stores [token].
  FutureOr<void> set(String token);

  /// Forgets the stored token.
  FutureOr<void> remove();

  /// An independent storage under [name], or null when this one keeps a
  /// single token.
  TokenStorage? slot(String name);

  /// The names of the slots that hold a token.
  FutureOr<List<String>> slots();
}

/// Keeps the token in memory: it is lost when the process ends. The default.
final class MemoryTokenStorage implements TokenStorage {
  String? _token;
  final _slots = <String, MemoryTokenStorage>{};

  @override
  Future<String?> get() async => _token;

  @override
  Future<void> set(String token) async => _token = token;

  @override
  Future<void> remove() async => _token = null;

  @override
  TokenStorage? slot(String name) =>
      _slots.putIfAbsent(name, MemoryTokenStorage.new);

  @override
  Future<List<String>> slots() async => [
        for (final e in _slots.entries)
          if (e.value._token != null) e.key,
      ];
}

/// Keeps the token in a file of the app's choosing — what a standalone Dart
/// program wants to stay signed in between runs:
/// `FileTokenStorage('${Platform.environment['HOME']}/.backd.session')`.
/// The file holds the token and nothing else, in plain text: keep it out of
/// version control and out of a directory other users read. The token of an
/// organization's client ([slot]) is in a file of its own beside it, named
/// `<path>.<organization>`.
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

  @override
  TokenStorage? slot(String name) => FileTokenStorage('$path.$name');

  @override
  Future<List<String>> slots() async {
    final file = File(path);
    final dir = file.parent;
    if (!await dir.exists()) return const [];
    final base = file.uri.pathSegments.last;
    final names = <String>[];
    await for (final e in dir.list()) {
      if (e is! File) continue;
      final name = e.uri.pathSegments.last;
      if (name.startsWith('$base.') &&
          await FileTokenStorage(e.path).get() != null) {
        names.add(name.substring(base.length + 1));
      }
    }
    return names;
  }
}
