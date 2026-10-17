import 'dart:async';

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
