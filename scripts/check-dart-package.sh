#!/usr/bin/env bash
# Checks the Dart client the way a user gets it from pub.dev: validates the
# package (what dart pub publish would upload), then adds it to a fresh Dart
# project and uses it — the client, the storages and the error types — from a
# script, exactly like the README's quickstart.
# Needs Dart (the Flutter SDK bundles it); run `flutter pub get` in
# clients/dart first.
#
#   scripts/check-dart-package.sh        # or: make dart-package
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root/clients/dart"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# What the package holds: the library, pubspec, README, CHANGELOG and LICENSE.
# `pub publish --dry-run` lists exactly the files it would upload, as a tree:
# rebuild the paths from the branch drawing.
list="$work/files.txt"
dart pub publish --dry-run 2>/dev/null | python3 -c '
import re, sys
dirs = []
for line in sys.stdin:
    m = re.match(r"^(.*)(?:├── |└── )([^()]+?)( \(.+\))?$", line)
    if not m:
        continue
    depth = len(m.group(1)) // 4
    name = m.group(2).strip()
    dirs = dirs[:depth]
    if m.group(3):  # files carry a "(N KB)" size, directories do not
        print("/".join(dirs + [name]))
    else:
        dirs.append(name)
' | sort > "$list" || {
  echo "dart pub publish --dry-run failed" >&2; dart pub publish --dry-run; exit 1;
}
[ -s "$list" ] || { echo "dart pub publish --dry-run failed" >&2; dart pub publish --dry-run; exit 1; }
for want in pubspec.yaml README.md CHANGELOG.md LICENSE lib/backd.dart lib/src/client.dart lib/src/auth.dart lib/src/data.dart lib/src/realtime.dart lib/src/typed.dart; do
  grep -qx "$want" "$list" || { echo "the package lacks $want" >&2; exit 1; }
done
if grep -Eq '^(example|\.dart_tool|bin)' "$list"; then
  echo "the package holds files it shouldn't:" >&2
  grep -E '^(example|\.dart_tool|bin)' "$list" >&2
  exit 1
fi
echo "package: $(wc -l <"$list") files"

# A fresh project that installs it the way pub.dev users will.
app="$work/smoke_app"   # dart create names the package after the directory
dart create --template console "$app" >/dev/null
cd "$app"
dart pub add "backd:{\"path\":\"$root/clients/dart\"}" >/dev/null

cat > bin/use.dart <<'DART'
// The package as a user sees it: client with a fake transport, token
// storage and the error hierarchy.
import 'dart:convert';
import 'dart:io';

import 'package:backd/backd.dart';

void main() async {
  final calls = <String>[];

  final client = BackdClient(
    url: 'https://api.example.com',
    realm: 'blog',
    transport: (req) async {
      calls.add('${req.method} ${req.url.path}');
      return TransportResponse(
        status: 200,
        body: utf8.encode('{"items": [], "next": null}'),
      );
    },
  );
  final page = await client.db('main').collection('posts').list(limit: 1);
  if (page.items.isNotEmpty || calls.length != 1) {
    throw StateError('unexpected calls: $calls');
  }

  // Token storages: memory and file.
  final storage = MemoryTokenStorage();
  await storage.set('tok');
  if (await storage.get() != 'tok') throw StateError('MemoryTokenStorage');
  final path =
      '${Directory.systemTemp.path}/smoke-${DateTime.now().microsecondsSinceEpoch}.session';
  final file = FileTokenStorage(path);
  await file.set('tok');
  if (await FileTokenStorage(path).get() != 'tok') {
    throw StateError('FileTokenStorage');
  }

  // The error hierarchy.
  const err = NotFoundError(status: 404, code: 'not_found', message: 'no post');
  if (err is! BackdException) {
    throw StateError('NotFoundError is not a BackdException');
  }

  stdout.writeln('dart: ok');
}
DART
dart analyze --fatal-warnings >/dev/null && echo "analyze: ok"
dart run bin/use.dart
echo "check-dart-package: ok"
