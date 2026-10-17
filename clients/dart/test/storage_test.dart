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
}
