import 'dart:async';

import 'package:backd/backd.dart';
import 'package:flutter/material.dart';

import 'backd/types.dart';
import 'main.dart';

/// The team's pins, kept live: the list a `LiveView` follows through resets
/// and reconnects. A pin is minted by the `pin` function (it enforces the
/// team's plan limit).
class PinsTab extends StatefulWidget {
  const PinsTab({super.key, required this.state});

  final AppState state;

  @override
  State<PinsTab> createState() => _PinsTabState();
}

class _PinsTabState extends State<PinsTab> {
  List<Map<String, Object?>> orgs = [];
  String? orgId;
  LiveView? view;
  StreamSubscription? sub;
  List<Map<String, Object?>> pins = [];
  LiveStatus status = LiveStatus.connecting;
  String? error;

  final title = TextEditingController();
  final url = TextEditingController();
  final note = TextEditingController();

  @override
  void initState() {
    super.initState();
    loadOrgs();
  }

  /// A session belongs to one team (the one signed in to): that is the team
  /// whose pins this tab follows.
  Future<void> loadOrgs() async {
    try {
      final team = await widget.state.client.auth.org();
      setState(() {
        orgs = team == null ? [] : [team];
        orgId = team == null ? null : '${team['id']}';
      });
      if (orgId != null) follow();
    } on BackdException catch (e) {
      setState(() => error = e.message);
    }
  }

  /// Follows the org's pins. Re-subscribes on a different pick.
  void follow() {
    sub?.cancel();
    view?.close();
    final org = orgId;
    if (org == null) return;
    final v = widget.state.client.realtime.subscribe([
      SubscriptionSpec(
        db: 'main',
        collection: 'pins',
        where: {'_meta.org': org},
        sort: '-_meta.created_at',
        limit: 50,
      ),
    ]);
    view = v;
    v.on('change', (_) {
      if (mounted) setState(() => pins = v.state.items);
    });
    v.on('status', (s) {
      if (mounted) setState(() => status = s as LiveStatus);
    });
    v.on('error', (e) {
      if (mounted) setState(() => error = '$e');
    });
    v.on('reset', (_) {
      // The view reloads itself; a status of resyncing covers the UX.
    });
    setState(() {
      pins = [];
      status = LiveStatus.connecting;
    });
  }

  Future<void> pin() async {
    final org = orgId;
    if (org == null || title.text.trim().isEmpty) return;
    try {
      final out = await widget.state.typed.main.pin(
        FnMainPinInput(
          org: org,
          title: title.text.trim(),
          url: url.text.trim().isEmpty ? null : url.text.trim(),
          note: note.text.trim().isEmpty ? null : note.text.trim(),
        ),
      );
      title.clear();
      url.clear();
      note.clear();
      if (mounted) {
        ScaffoldMessenger.of(
          context,
        ).showSnackBar(SnackBar(content: Text('Pinned — ${out.pins}/${out.limit} used')));
      }
    } on BackdException catch (e) {
      setState(() => error = '${e.code}: ${e.message}');
    }
  }

  @override
  void dispose() {
    sub?.cancel();
    view?.close();
    title.dispose();
    url.dispose();
    note.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final statusLabel = switch (status) {
      LiveStatus.connecting => 'connecting…',
      LiveStatus.live => 'live',
      LiveStatus.resyncing => 'resyncing…',
      LiveStatus.closed => 'closed',
    };
    return Column(
      children: [
        Padding(
          padding: const EdgeInsets.all(12),
          child: Row(
            children: [
              Expanded(
                child: orgs.isEmpty
                    ? const Text(
                        'No teams yet — pins belong to a team. Create one with `backd` or the JS demo first.',
                      )
                    : DropdownButtonFormField<String>(
                        initialValue: orgId,
                        decoration: const InputDecoration(
                          labelText: 'Team',
                          border: OutlineInputBorder(),
                          isDense: true,
                        ),
                        items: [
                          for (final o in orgs)
                            DropdownMenuItem(
                              value: '${o['id']}',
                              child: Text('${o['name'] ?? o['id']}'),
                            ),
                        ],
                        onChanged: (v) => setState(() {
                          orgId = v;
                          follow();
                        }),
                      ),
              ),
              const SizedBox(width: 12),
              Chip(
                avatar: Icon(
                  status == LiveStatus.live ? Icons.circle : Icons.circle_outlined,
                  size: 12,
                  color: status == LiveStatus.live ? Colors.green : Colors.grey,
                ),
                label: Text(statusLabel),
              ),
            ],
          ),
        ),
        Padding(
          padding: const EdgeInsets.symmetric(horizontal: 12),
          child: Row(
            children: [
              Expanded(
                child: TextField(
                  controller: title,
                  decoration: const InputDecoration(labelText: 'Title', isDense: true),
                ),
              ),
              const SizedBox(width: 8),
              Expanded(
                child: TextField(
                  controller: url,
                  decoration: const InputDecoration(labelText: 'URL', isDense: true),
                ),
              ),
              const SizedBox(width: 8),
              Expanded(
                child: TextField(
                  controller: note,
                  decoration: const InputDecoration(labelText: 'Note', isDense: true),
                ),
              ),
              const SizedBox(width: 8),
              FilledButton(onPressed: pin, child: const Text('Pin')),
            ],
          ),
        ),
        if (error != null)
          Padding(
            padding: const EdgeInsets.all(8),
            child: Text(
              error!,
              style: TextStyle(color: Theme.of(context).colorScheme.error),
            ),
          ),
        const SizedBox(height: 8),
        Expanded(
          child: pins.isEmpty
              ? Center(
                  child: Text(status == LiveStatus.live ? 'Nothing pinned.' : 'Loading…'),
                )
              : ListView.builder(
                  itemCount: pins.length,
                  itemBuilder: (context, i) {
                    final p = pins[i];
                    return ListTile(
                      leading: const Icon(Icons.push_pin_outlined),
                      title: Text('${p['title']}'),
                      subtitle: Text(
                        [
                          if (p['url'] != null) '${p['url']}',
                          if (p['note'] != null) '${p['note']}',
                          if (p['author'] != null) 'by ${p['author']}',
                        ].join(' · '),
                      ),
                    );
                  },
                ),
        ),
      ],
    );
  }
}
