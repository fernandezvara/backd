# backd

The pure-Dart client for [backd](https://github.com/fernandezvara/backd):
sign-in and sessions, collections and the query language, files, function
jobs, realtime live views, and the realm's generated types. Pure Dart — it
runs in Flutter apps and in standalone Dart programs alike.

> **Early development:** the API surface is being built module by module
> against `backd-js`. See `design/dart-client.md` in the repository for the
> plan and what each `0.0.x` release covers.

```dart
import 'package:backd/backd.dart';

final backd = BackdClient(
  url: 'http://localhost:8080',
  realm: 'shelf',
  storage: FileTokenStorage('${Platform.environment['HOME']}/.backd.session'),
);

final page = await backd.db('main').collection('assets').list(limit: 20);
for (final doc in page.items) print(doc['title']);
```

The documentation lives in the
[docs site](https://docs.backd.dev/build/clients/dart/) once the client guide
lands; until then the [JavaScript guide](https://docs.backd.dev/build/clients/js/)
describes the same API concepts.
