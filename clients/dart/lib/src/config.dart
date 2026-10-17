import 'dart:async';

/// Where `BackdClient.config` keeps its copies between runs: a file for
/// standalone apps, a SharedPreferences-backed store for Flutter (supplied
/// by the app so `backd` stays pure Dart). Memory only when not given.
abstract interface class ConfigStorage {
  /// The value for [key], or null.
  FutureOr<String?> getItem(String key);

  /// Stores [value] under [key].
  FutureOr<void> setItem(String key, String value);

  /// Forgets [key].
  FutureOr<void> removeItem(String key);
}
