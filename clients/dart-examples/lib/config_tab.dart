import 'package:backd/backd.dart';
import 'package:flutter/material.dart';

import 'main.dart';

/// The realm's app configuration through the typed getters of `ShelfConfig`:
/// `features.export.enabled`, `maintenance.enabled`, `maintenance.message`.
/// `refresh: true` goes to the server; without it the cache answers.
class ConfigTab extends StatefulWidget {
  const ConfigTab({super.key, required this.state});

  final AppState state;

  @override
  State<ConfigTab> createState() => _ConfigTabState();
}

class _ConfigTabState extends State<ConfigTab> {
  bool? exportEnabled;
  bool? maintenance;
  String? message;
  String? error;
  bool cached = true;

  Future<void> load({bool refresh = false}) async {
    final c = widget.state.typed.config;
    try {
      final results = await Future.wait([
        c.featuresExportEnabled(refresh: refresh),
        c.maintenanceEnabled(refresh: refresh),
        c.maintenanceMessage(refresh: refresh),
      ]);
      setState(() {
        exportEnabled = results[0] as bool?;
        maintenance = results[1] as bool?;
        message = results[2] as String?;
        cached = !refresh;
        error = null;
      });
    } on BackdException catch (e) {
      setState(() => error = '${e.code}: ${e.message}');
    }
  }

  @override
  void initState() {
    super.initState();
    load();
  }

  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.all(16),
    child: Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Row(
          children: [
            Text('App configuration', style: Theme.of(context).textTheme.titleLarge),
            const Spacer(),
            if (cached) const Chip(label: Text('cached')),
            const SizedBox(width: 8),
            FilledButton.tonal(
              onPressed: () => load(refresh: true),
              child: const Text('Refresh from server'),
            ),
          ],
        ),
        const SizedBox(height: 16),
        if (error != null)
          Text(error!, style: TextStyle(color: Theme.of(context).colorScheme.error)),
        ListTile(
          leading: const Icon(Icons.download_outlined),
          title: const Text('features.export.enabled'),
          trailing: Text('${exportEnabled ?? '—'}'),
        ),
        ListTile(
          leading: Icon(
            maintenance == true ? Icons.construction : Icons.check_circle_outline,
          ),
          title: const Text('maintenance.enabled'),
          trailing: Text('${maintenance ?? '—'}'),
        ),
        if (maintenance == true)
          ListTile(
            leading: const Icon(Icons.message_outlined),
            title: const Text('maintenance.message'),
            subtitle: Text(message ?? ''),
          ),
        const Divider(height: 32),
        Text(
          'These come from `client.typed.config` — the getters '
          '`backd types --lang dart` generated for the realm\'s '
          'app-config paths.',
          style: Theme.of(context).textTheme.bodySmall,
        ),
      ],
    ),
  );
}
