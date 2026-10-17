/// The pure-Dart client for backd: sign-in and sessions, collections and the
/// query language, files, function jobs, realtime live views and generated
/// realm types. Works in Flutter apps and standalone Dart programs alike.
///
/// ```dart
/// import 'package:backd/backd.dart';
///
/// final backd = BackdClient(url: 'http://localhost:8080', realm: 'shelf');
/// ```
///
/// There is deliberately no admin API: administration is JavaScript-only
/// (`backd-js`, the CLI, the admin UI) so that credentials with realm-wide
/// power never reach an app that runs on someone's device.
library;

export 'src/auth.dart';
export 'src/cache.dart' show CacheEntry, CacheOptions, CacheStats, RevalidationCache;
export 'src/client.dart';
export 'src/config.dart' show ConfigStorage;
export 'src/data.dart';
export 'src/errors.dart';
export 'src/oauth.dart' show OAuthStorage, MemoryOAuthStorage;
export 'src/storage.dart';
export 'src/transport.dart';
