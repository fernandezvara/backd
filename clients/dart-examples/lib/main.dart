import 'package:backd/backd.dart';
import 'package:flutter/material.dart';

import 'assets.dart';
import 'backd/types.dart';
import 'config_tab.dart';
import 'pins.dart';
import 'signin.dart';

/// The Shelf demo state: the client, who is signed in, and the realm's typed
/// handle (`state.typed.main.assets` and friends — generated types).
///
/// A session belongs to one team (chapter 20), so a person who works in several
/// holds one session each: [root] keeps them all, [client] is the one of the team
/// this screen works in, and [teams] feeds the switcher.
class AppState extends ChangeNotifier {
  AppState({required this.url})
    : root = BackdClient<ShelfRealm>(
        url: url,
        realm: 'shelf',
        storage: MemoryTokenStorage(),
      );

  final String url;
  final BackdClient<ShelfRealm> root;

  /// The team this screen works in; null for the operator, who belongs to none.
  String? team;

  /// The teams this device holds a session for (the API never lists others).
  List<({String ref, String? id, String? expiresAt, String? name})> teams =
      const [];

  /// Signing in to one more team while the others stay signed in.
  bool adding = false;

  BackdClient<ShelfRealm> get client => team == null ? root : root.org(team!);

  ShelfRealm get typed => client.typed;

  Me? me;
  bool booted = false;

  /// Restores the stored session, if any.
  Future<void> boot() async {
    try {
      me = await root.auth.me();
    } on BackdException {
      me = null;
    }
    booted = true;
    notifyListeners();
  }

  /// A sign-in to [ref] (null: the realm, for an operator) finished.
  Future<void> signedIn([String? ref]) async {
    team = ref;
    adding = false;
    me = await client.auth.me();
    if (ref != null) {
      await client.auth.org(); // keeps the team's name for the switcher
    }
    teams = await root.orgs.signedIn();
    notifyListeners();
  }

  /// Another team of this device: its session is already here.
  Future<void> switchTo(String ref) async {
    team = ref;
    me = await client.auth.me();
    notifyListeners();
  }

  void addTeam() {
    adding = true;
    notifyListeners();
  }

  void cancelAdding() {
    adding = false;
    notifyListeners();
  }

  /// Signs out of this team only; the others stay signed in.
  Future<void> signOut() async {
    await client.auth.logout().catchError((_) {});
    teams = await root.orgs.signedIn();
    final rest = teams.where((t) => t.ref != team).toList();
    team = null;
    me = null;
    if (rest.isNotEmpty) {
      try {
        await switchTo(rest.first.ref);
        return;
      } on BackdException {
        team = null;
      }
    }
    notifyListeners();
  }

  bool get isCurator => me?.roles.contains(ShelfRole.curator) ?? false;
}

void main() => runApp(const ShelfApp());

class ShelfApp extends StatelessWidget {
  const ShelfApp({super.key});

  @override
  Widget build(BuildContext context) => MaterialApp(
    title: 'Shelf',
    debugShowCheckedModeBanner: false,
    theme: ThemeData(
      colorScheme: ColorScheme.fromSeed(seedColor: const Color(0xFF4a6fa5)),
      useMaterial3: true,
    ),
    home: const _Gate(),
  );
}

/// Asks which backd to talk to, then signs in, then shows the app.
class _Gate extends StatefulWidget {
  const _Gate();

  @override
  State<_Gate> createState() => _GateState();
}

class _GateState extends State<_Gate> {
  AppState? state;

  @override
  Widget build(BuildContext context) {
    final s = state;
    if (s == null) {
      return ConnectScreen(
        onConnect: (url) => setState(() {
          state = AppState(url: url)..boot();
        }),
      );
    }
    return AnimatedBuilder(
      animation: s,
      builder: (context, _) {
        if (!s.booted) {
          return const Scaffold(
            body: Center(child: CircularProgressIndicator()),
          );
        }
        if (s.me == null || s.adding) {
          return SignInScreen(state: s);
        }
        return HomeScreen(key: ValueKey(s.team), state: s);
      },
    );
  }
}

/// Where the backd runs. The dev stack serves one at :8080.
class ConnectScreen extends StatefulWidget {
  const ConnectScreen({super.key, required this.onConnect});

  final void Function(String url) onConnect;

  @override
  State<ConnectScreen> createState() => _ConnectScreenState();
}

class _ConnectScreenState extends State<ConnectScreen> {
  final url = TextEditingController(text: 'http://localhost:8080');

  @override
  Widget build(BuildContext context) => Scaffold(
    body: Center(
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 380),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Text('Shelf', style: Theme.of(context).textTheme.displayMedium),
            const SizedBox(height: 8),
            const Text('A team asset library on backd.'),
            const SizedBox(height: 24),
            TextField(
              controller: url,
              decoration: const InputDecoration(
                labelText: 'backd URL',
                hintText: 'http://localhost:8080',
                border: OutlineInputBorder(),
              ),
              onSubmitted: (v) => widget.onConnect(v.trim()),
            ),
            const SizedBox(height: 12),
            FilledButton(
              onPressed: () => widget.onConnect(url.text.trim()),
              child: const Text('Connect'),
            ),
          ],
        ),
      ),
    ),
  );
}

/// The app: gallery, drafts, the curator's queue, live pins, configuration.
class HomeScreen extends StatefulWidget {
  const HomeScreen({super.key, required this.state});

  final AppState state;

  @override
  State<HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends State<HomeScreen> {
  var tab = 0;

  @override
  Widget build(BuildContext context) {
    final s = widget.state;
    final destinations = <NavigationRailDestination>[
      const NavigationRailDestination(
        icon: Icon(Icons.photo_library_outlined),
        label: Text('Gallery'),
      ),
      const NavigationRailDestination(
        icon: Icon(Icons.edit_note),
        label: Text('My drafts'),
      ),
      if (s.isCurator)
        const NavigationRailDestination(
          icon: Icon(Icons.fact_check_outlined),
          label: Text('Review'),
        ),
      const NavigationRailDestination(
        icon: Icon(Icons.push_pin_outlined),
        label: Text('Pins'),
      ),
      const NavigationRailDestination(
        icon: Icon(Icons.settings_outlined),
        label: Text('Config'),
      ),
    ];
    final pages = <Widget>[
      GalleryTab(state: s),
      DraftsTab(state: s),
      if (s.isCurator) ReviewTab(state: s),
      PinsTab(state: s),
      ConfigTab(state: s),
    ];
    if (tab >= pages.length) tab = 0;
    return Scaffold(
      appBar: AppBar(
        title: const Text('Shelf'),
        actions: [
          if (s.team != null)
            PopupMenuButton<String>(
              tooltip: 'Switch team',
              icon: const Icon(Icons.groups_outlined),
              onSelected: (ref) => ref == '' ? s.addTeam() : s.switchTo(ref),
              itemBuilder: (_) => [
                for (final t in s.teams)
                  CheckedPopupMenuItem(
                    value: t.ref,
                    checked: t.ref == s.team,
                    child: Text(t.name ?? t.ref),
                  ),
                const PopupMenuDivider(),
                const PopupMenuItem(
                  value: '',
                  child: Text('Sign in to another team…'),
                ),
              ],
            ),
          Center(
            child: Text(
              s.me?.email ?? '',
              style: Theme.of(context).textTheme.bodyMedium,
            ),
          ),
          const SizedBox(width: 12),
          IconButton(
            tooltip: 'Sign out',
            icon: const Icon(Icons.logout),
            onPressed: s.signOut,
          ),
        ],
      ),
      body: Row(
        children: [
          NavigationRail(
            selectedIndex: tab,
            onDestinationSelected: (i) => setState(() => tab = i),
            labelType: NavigationRailLabelType.all,
            destinations: destinations,
          ),
          const VerticalDivider(width: 1),
          Expanded(child: pages[tab]),
        ],
      ),
    );
  }
}
