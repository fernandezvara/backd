import 'dart:io';

import 'package:backd/backd.dart';
import 'package:test/test.dart';

void main() {
  test('MemoryTokenStorage round-trips and forgets', () async {
    final storage = MemoryTokenStorage();
    expect(await storage.get(), isNull);
    await storage.set('tok');
    expect(await storage.get(), 'tok');
    await storage.remove();
    expect(await storage.get(), isNull);
  });

  test('FileTokenStorage keeps the token between instances', () async {
    final path =
        '${Directory.systemTemp.path}/backd-storage-${DateTime.now().microsecondsSinceEpoch}.session';
    addTearDown(() => File(path).delete().catchError((_) => File(path)));

    var storage = FileTokenStorage(path);
    expect(await storage.get(), isNull);

    await storage.set('tok');
    expect(await File(path).readAsString(), 'tok');

    storage = FileTokenStorage(path); // a later run reads the same file
    expect(await storage.get(), 'tok');

    await storage.remove();
    expect(await storage.get(), isNull);
    await storage.remove(); // removing nothing is fine
  });
}
