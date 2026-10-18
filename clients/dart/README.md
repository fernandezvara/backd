# backd

The pure-Dart client for [backd](https://github.com/fernandezvara/backd):
sign-in and sessions — the second-factor, enrollment and consent steps
included — magic links and sign-in with a provider, collections and the query
language, files (uploads, versions, signed links), function jobs,
organizations and teams, push devices, consents, a cached app configuration,
realtime live views, and the realm's generated types. Pure Dart — it runs in
Flutter apps and in standalone Dart programs alike.

## Install

```console
$ dart pub add backd     # or: flutter pub add backd
```

The package's only dependencies are Dart-team packages (`crypto` for file
checksums). It needs a `dart:io` platform — Flutter mobile and desktop, and
standalone Dart; Flutter web is not supported yet.

## A standalone script

```dart
// bin/shelf.dart — `SHELF_EMAIL=ada@example.com SHELF_PASSWORD=… dart run`
import 'dart:io';

import 'package:backd/backd.dart';

Future<void> main() async {
  final backd = BackdClient(
    url: 'http://localhost:8080',   // the tutorial stack: nginx in front of backd
    realm: 'shelf',
    storage: FileTokenStorage(
        '${Platform.environment['HOME']}/.backd-shelf.session'),
  );

  // The token lives in the file above: only the first run logs in.
  if (!await backd.auth.hasSession()) {
    await backd.auth.login(
      Platform.environment['SHELF_EMAIL']!,
      Platform.environment['SHELF_PASSWORD']!,
    );
  }

  final page = await backd.db('main').collection('assets').list(
        where: {'published_at': {'$notNull': true}},
        orderBy: '-published_at',
        limit: 20,
      );

  for (final doc in page.items) {
    final tags = (doc['tags'] as List? ?? []).join(', ');
    stdout.writeln(
        '• ${doc['title']} [${doc['kind']}] ${doc['url'] ?? ''} $tags');
  }
  stdout.writeln(
      '--- ${page.items.length} published assets${page.hasMore ? ', more…' : ''}');

  await backd.close();
}
```

```text
• Backd's query language [link] https://backd.dev/docs/build/api/querying docs, backend
• Release checklist [note]
--- 2 published assets
```

## The realm's types

`backd types --realm shelf --lang dart` writes `types.dart` from the realm's
config — a `ShelfRealm` with every collection, function and configuration
path typed:

```dart
import 'backd/types.dart';   // generated

final client = BackdClient<ShelfRealm>(url: url, realm: 'shelf');
final page = await client.typed.main.assets.list(
  where: {MainAssetsField.publishedAt: {'$ne': null}},
);
```

## Documentation and the demo

The [Dart client guide](https://docs.backd.dev/docs/clients/dart/) covers the
sign-in step machine, OAuth on desktop, token storage, collections and the
query language, caches, realtime, files, functions, organizations, consents,
errors and retries. The [Shelf demo](example/) in `example/` is a Flutter
desktop app exercising all of it.

> **No admin API, by design.** Administration is for the operator's own
> tooling — the CLI, the admin UI and functions, all JavaScript. A client
> application holding admin capability is a footgun; a Dart program that
> truly needs the admin API calls the REST endpoints directly, knowingly.

## Versions

`0.0.x` while the client's surface settles: breaking changes land without a
major bump, so pin the version (`backd: ^0.0.1` gets fixes only) and read the
CHANGELOG on upgrades. Once the client reaches parity with the server and
`backd-js`, its versions align with their release train — a `backd` X.Y will
always match backd X.Y, like `backd-js` does today.

## License

See the repository's
[LICENSE](https://github.com/fernandezvara/backd/blob/main/LICENSE).
