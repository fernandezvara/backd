import 'dart:io';

import 'package:backd/backd.dart';
import 'package:file_picker/file_picker.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import 'backd/types.dart';
import 'main.dart';

/// Published assets — what every member sees.
class GalleryTab extends StatefulWidget {
  const GalleryTab({super.key, required this.state});

  final AppState state;

  @override
  State<GalleryTab> createState() => _GalleryTabState();
}

class _GalleryTabState extends State<GalleryTab> {
  List<MainAssets> items = [];
  String tag = '';
  String? error;

  Future<void> load() async {
    try {
      final page = await widget.state.typed.main.assets.list(
        where: {
          MainAssetsField.publishedAt: {'\$ne': null},
          if (tag.isNotEmpty) MainAssetsField.tags: tag,
        },
        orderBy: '-${MainAssetsField.publishedAt}',
        limit: 50,
        expand: 'author',
      );
      setState(() {
        items = page.items;
        error = null;
      });
    } on BackdException catch (e) {
      setState(() => error = e.message);
    }
  }

  @override
  void initState() {
    super.initState();
    load();
  }

  @override
  Widget build(BuildContext context) => Column(
    children: [
      Padding(
        padding: const EdgeInsets.all(12),
        child: Row(
          children: [
            Expanded(
              child: TextField(
                decoration: const InputDecoration(
                  labelText: 'Filter by tag',
                  border: OutlineInputBorder(),
                  isDense: true,
                ),
                onChanged: (v) => tag = v.trim(),
                onSubmitted: (_) => load(),
              ),
            ),
            const SizedBox(width: 8),
            FilledButton.tonal(onPressed: load, child: const Text('Refresh')),
          ],
        ),
      ),
      if (error != null)
        Text(error!, style: TextStyle(color: Theme.of(context).colorScheme.error)),
      Expanded(
        child: items.isEmpty
            ? const Center(child: Text('Nothing published yet.'))
            : ListView.builder(
                itemCount: items.length,
                itemBuilder: (context, i) =>
                    AssetCard(asset: items[i], state: widget.state, onChanged: load),
              ),
      ),
    ],
  );
}

/// One asset: its body, files, a link to open, and a share-link minter.
class AssetCard extends StatelessWidget {
  const AssetCard({
    super.key,
    required this.asset,
    required this.state,
    this.onChanged,
    this.actions = const [],
  });

  final MainAssets asset;
  final AppState state;
  final void Function()? onChanged;
  final List<Widget> actions;

  Future<void> share(BuildContext context, String expiry) async {
    try {
      final out = await state.typed.main.share(
        FnMainShareInput(
          assetId: asset.id,
          expiresIn: FnMainShareInputExpiresIn.maybeValue(expiry),
        ),
      );
      final token = (out as Map)['token'];
      if (!context.mounted) return;
      await showDialog<void>(
        context: context,
        builder: (c) => AlertDialog(
          title: const Text('Share link'),
          content: SelectableText(
            'share token:\n$token\n\n'
            'Anyone opening it with `share-open` sees the asset and its files, no account needed.',
          ),
          actions: [
            TextButton(
              onPressed: () => Clipboard.setData(ClipboardData(text: '$token')),
              child: const Text('Copy token'),
            ),
            FilledButton(onPressed: () => Navigator.pop(c), child: const Text('Done')),
          ],
        ),
      );
    } on BackdException catch (e) {
      if (context.mounted) {
        ScaffoldMessenger.of(
          context,
        ).showSnackBar(SnackBar(content: Text('${e.code}: ${e.message}')));
      }
    }
  }

  @override
  Widget build(BuildContext context) => Card(
    margin: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
    child: Padding(
      padding: const EdgeInsets.all(12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Chip(label: Text(asset.kind.value), visualDensity: VisualDensity.compact),
              const SizedBox(width: 8),
              Expanded(
                child: Text(asset.title, style: Theme.of(context).textTheme.titleMedium),
              ),
              if (asset.publishedAt != null)
                Text(
                  'published ${asset.publishedAt!.value}',
                  style: Theme.of(context).textTheme.bodySmall,
                ),
            ],
          ),
          if (asset.expanded?.author?.name != null)
            Text(
              'by ${asset.expanded!.author!.name}',
              style: Theme.of(context).textTheme.bodySmall,
            ),
          if (asset.url != null)
            Padding(
              padding: const EdgeInsets.only(top: 4),
              child: SelectableText(
                asset.url!,
                style: TextStyle(color: Theme.of(context).colorScheme.primary),
              ),
            ),
          if (asset.body != null)
            Padding(padding: const EdgeInsets.only(top: 8), child: Text(asset.body!)),
          if (asset.tags != null && asset.tags!.isNotEmpty)
            Padding(
              padding: const EdgeInsets.only(top: 8),
              child: Wrap(
                spacing: 4,
                children: [
                  for (final t in asset.tags!)
                    Chip(label: Text(t), visualDensity: VisualDensity.compact),
                ],
              ),
            ),
          if (asset.file != null || (asset.attachments?.isNotEmpty ?? false))
            Padding(
              padding: const EdgeInsets.only(top: 8),
              child: Wrap(
                spacing: 8,
                children: [
                  if (asset.file != null)
                    FileChip(
                      asset: asset,
                      file: asset.file!,
                      field: 'file',
                      state: state,
                    ),
                  for (final f in asset.attachments ?? <FileDetails>[])
                    FileChip(asset: asset, file: f, field: 'attachments', state: state),
                ],
              ),
            ),
          OverflowBar(
            spacing: 8,
            children: [
              PopupMenuButton<String>(
                onSelected: (expiry) => share(context, expiry),
                itemBuilder: (c) => const [
                  PopupMenuItem(value: 'week', child: Text('Share for a week')),
                  PopupMenuItem(value: 'month', child: Text('Share for a month')),
                  PopupMenuItem(value: 'never', child: Text('Share forever')),
                ],
                child: const Padding(
                  padding: EdgeInsets.all(8),
                  child: Row(
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      Icon(Icons.share, size: 18),
                      SizedBox(width: 4),
                      Text('Share'),
                    ],
                  ),
                ),
              ),
              ...actions,
            ],
          ),
        ],
      ),
    ),
  );
}

/// A file of an asset: name, size, a link the server signs.
class FileChip extends StatefulWidget {
  const FileChip({
    super.key,
    required this.asset,
    required this.file,
    required this.field,
    required this.state,
  });

  final MainAssets asset;
  final FileDetails file;
  final String field;
  final AppState state;

  @override
  State<FileChip> createState() => _FileChipState();
}

class _FileChipState extends State<FileChip> {
  bool busy = false;

  Future<void> open() async {
    setState(() => busy = true);
    try {
      final link = await widget.state.typed.main.assets.fileUrl(
        widget.asset.id,
        widget.field,
        widget.file.id,
      );
      if (!mounted) return;
      await showDialog<void>(
        context: context,
        builder: (c) => AlertDialog(
          title: Text(widget.file.name),
          content: SelectableText(link?.url ?? 'no link'),
          actions: [
            FilledButton(onPressed: () => Navigator.pop(c), child: const Text('Done')),
          ],
        ),
      );
    } on BackdException catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(e.message)));
      }
    } finally {
      if (mounted) setState(() => busy = false);
    }
  }

  @override
  Widget build(BuildContext context) => ActionChip(
    avatar: busy
        ? const SizedBox(
            width: 14,
            height: 14,
            child: CircularProgressIndicator(strokeWidth: 2),
          )
        : const Icon(Icons.attach_file, size: 16),
    label: Text('${widget.file.name} (${(widget.file.size / 1024).toStringAsFixed(0)}K)'),
    onPressed: open,
  );
}

/// The member's drafts: create, edit, upload a file, delete, submit for review.
class DraftsTab extends StatefulWidget {
  const DraftsTab({super.key, required this.state});

  final AppState state;

  @override
  State<DraftsTab> createState() => _DraftsTabState();
}

class _DraftsTabState extends State<DraftsTab> {
  List<MainAssets> mine = [];
  String? error;

  Future<void> load() async {
    try {
      final page = await widget.state.typed.main.assets.list(
        where: {
          MainAssetsField.publishedAt: null,
          MainAssetsField.metaOwner: widget.state.me?.id,
        },
        orderBy: '-${MainAssetsField.metaCreatedAt}',
        limit: 50,
      );
      setState(() {
        mine = page.items;
        error = null;
      });
    } on BackdException catch (e) {
      setState(() => error = e.message);
    }
  }

  @override
  void initState() {
    super.initState();
    load();
  }

  Future<void> create() async {
    final draft = await showDialog<MainAssetsInput>(
      context: context,
      builder: (_) => const AssetDialog(),
    );
    if (draft == null) return;
    try {
      await widget.state.typed.main.assets.create(draft);
      load();
    } on BackdException catch (e) {
      setState(() => error = '${e.code}: ${e.message}');
    }
  }

  Future<void> edit(MainAssets a) async {
    final draft = await showDialog<MainAssetsInput>(
      context: context,
      builder: (_) => AssetDialog(asset: a),
    );
    if (draft == null) return;
    try {
      await widget.state.typed.main.assets.replace(a.id, draft, ifMatch: a.meta.version);
      load();
    } on BackdException catch (e) {
      setState(() => error = '${e.code}: ${e.message}');
    }
  }

  Future<void> remove(MainAssets a) async {
    try {
      await widget.state.typed.main.assets.delete(a.id);
      load();
    } on BackdException catch (e) {
      setState(() => error = e.message);
    }
  }

  Future<void> upload(MainAssets a) async {
    final picked = await FilePicker.pickFiles();
    final path = picked?.files.single.path;
    if (path == null) return;
    final bytes = await File(path).readAsBytes();
    try {
      await widget.state.typed.main.assets.uploadFile(
        a.id,
        'file',
        UploadData.of(bytes, name: path.split('/').last),
      );
      load();
    } on BackdException catch (e) {
      setState(() => error = e.message);
    }
  }

  Future<void> submitForReview(MainAssets a) async {
    try {
      await widget.state.typed.main.submissions.create(
        MainSubmissionsInput(assetId: a.id, status: MainSubmissionsInputStatus.submitted),
      );
      if (mounted) {
        ScaffoldMessenger.of(
          context,
        ).showSnackBar(const SnackBar(content: Text('Submitted for review')));
      }
    } on BackdException catch (e) {
      setState(() => error = '${e.code}: ${e.message}');
    }
  }

  @override
  Widget build(BuildContext context) => Column(
    children: [
      Padding(
        padding: const EdgeInsets.all(12),
        child: Row(
          children: [
            FilledButton.icon(
              onPressed: create,
              icon: const Icon(Icons.add),
              label: const Text('New asset'),
            ),
            const SizedBox(width: 8),
            FilledButton.tonal(onPressed: load, child: const Text('Refresh')),
          ],
        ),
      ),
      if (error != null)
        Text(error!, style: TextStyle(color: Theme.of(context).colorScheme.error)),
      Expanded(
        child: mine.isEmpty
            ? const Center(child: Text('No drafts yet.'))
            : ListView.builder(
                itemCount: mine.length,
                itemBuilder: (context, i) {
                  final a = mine[i];
                  return AssetCard(
                    asset: a,
                    state: widget.state,
                    onChanged: load,
                    actions: [
                      TextButton.icon(
                        onPressed: () => edit(a),
                        icon: const Icon(Icons.edit, size: 16),
                        label: const Text('Edit'),
                      ),
                      TextButton.icon(
                        onPressed: () => upload(a),
                        icon: const Icon(Icons.upload_file, size: 16),
                        label: const Text('Attach file'),
                      ),
                      TextButton.icon(
                        onPressed: () => submitForReview(a),
                        icon: const Icon(Icons.send, size: 16),
                        label: const Text('Submit'),
                      ),
                      TextButton.icon(
                        onPressed: () => remove(a),
                        icon: const Icon(Icons.delete_outline, size: 16),
                        label: const Text('Delete'),
                      ),
                    ],
                  );
                },
              ),
      ),
    ],
  );
}

/// The curator's queue: every draft, publish it — or the submission decides.
class ReviewTab extends StatefulWidget {
  const ReviewTab({super.key, required this.state});

  final AppState state;

  @override
  State<ReviewTab> createState() => _ReviewTabState();
}

class _ReviewTabState extends State<ReviewTab> {
  List<MainAssets> queue = [];
  List<MainSubmissions> submissions = [];
  String? error;

  Future<void> load() async {
    try {
      final page = await widget.state.typed.main.assets.list(
        where: {MainAssetsField.publishedAt: null},
        orderBy: '-${MainAssetsField.metaCreatedAt}',
        limit: 50,
      );
      final subs = await widget.state.typed.main.submissions.list(
        where: {MainSubmissionsField.status: MainSubmissionsStatus.submitted.value},
        orderBy: '-${MainSubmissionsField.metaCreatedAt}',
        limit: 50,
      );
      setState(() {
        queue = page.items;
        submissions = subs.items;
        error = null;
      });
    } on BackdException catch (e) {
      setState(() => error = e.message);
    }
  }

  @override
  void initState() {
    super.initState();
    load();
  }

  Future<void> publish(MainAssets a) async {
    try {
      await widget.state.typed.main.publish(
        FnMainPublishInput(assetId: a.id),
        idempotencyKey: 'publish-${a.id}',
      );
      load();
    } on BackdException catch (e) {
      setState(() => error = '${e.code}: ${e.message}');
    }
  }

  Future<void> decide(MainSubmissions s, String status) async {
    try {
      await widget.state.typed.main.submissions.patch(s.id, {
        'status': status,
        'decided_by': widget.state.me?.id,
      });
      load();
    } on BackdException catch (e) {
      setState(() => error = '${e.code}: ${e.message}');
    }
  }

  Future<void> dismiss(MainSubmissions s) async {
    try {
      await widget.state.typed.main.submissions.delete(s.id);
      load();
    } on BackdException catch (e) {
      setState(() => error = '${e.code}: ${e.message}');
    }
  }

  @override
  Widget build(BuildContext context) => ListView(
    children: [
      Padding(
        padding: const EdgeInsets.all(12),
        child: Row(
          children: [
            Text('Review queue', style: Theme.of(context).textTheme.titleLarge),
            const Spacer(),
            FilledButton.tonal(onPressed: load, child: const Text('Refresh')),
          ],
        ),
      ),
      if (error != null)
        Text(error!, style: TextStyle(color: Theme.of(context).colorScheme.error)),
      if (submissions.isNotEmpty) ...[
        Padding(
          padding: const EdgeInsets.all(12),
          child: Text('Submissions', style: Theme.of(context).textTheme.titleMedium),
        ),
        for (final s in submissions)
          ListTile(
            leading: const Icon(Icons.rate_review_outlined),
            title: Text('asset ${s.assetId}'),
            subtitle: Text('${s.status.value} · by ${s.meta.createdBy ?? '?'}'),
            trailing: Wrap(
              spacing: 8,
              children: [
                TextButton(
                  onPressed: () => decide(s, 'approved'),
                  child: const Text('Approve'),
                ),
                TextButton(onPressed: () => dismiss(s), child: const Text('Dismiss')),
              ],
            ),
          ),
      ],
      for (final a in queue)
        AssetCard(
          asset: a,
          state: widget.state,
          actions: [
            FilledButton.icon(
              onPressed: () => publish(a),
              icon: const Icon(Icons.publish, size: 16),
              label: const Text('Publish'),
            ),
          ],
        ),
      if (queue.isEmpty && submissions.isEmpty)
        const Padding(
          padding: EdgeInsets.all(24),
          child: Center(child: Text('Nothing waiting for review.')),
        ),
    ],
  );
}

/// New-asset / edit dialog: the `MainAssetsInput` fields, typed.
class AssetDialog extends StatefulWidget {
  const AssetDialog({super.key, this.asset});

  final MainAssets? asset;

  @override
  State<AssetDialog> createState() => _AssetDialogState();
}

class _AssetDialogState extends State<AssetDialog> {
  late final title = TextEditingController(text: widget.asset?.title ?? '');
  late final url = TextEditingController(text: widget.asset?.url ?? '');
  late final body = TextEditingController(text: widget.asset?.body ?? '');
  late final tags = TextEditingController(text: widget.asset?.tags?.join(', ') ?? '');
  late var kind = widget.asset?.kind ?? MainAssetsKind.note;

  @override
  Widget build(BuildContext context) => AlertDialog(
    title: Text(widget.asset == null ? 'New asset' : 'Edit asset'),
    content: SizedBox(
      width: 420,
      child: ListView(
        shrinkWrap: true,
        children: [
          TextField(
            controller: title,
            decoration: const InputDecoration(labelText: 'Title'),
          ),
          const SizedBox(height: 12),
          DropdownButtonFormField<MainAssetsKind>(
            initialValue: kind,
            decoration: const InputDecoration(labelText: 'Kind'),
            items: [
              for (final k in MainAssetsKind.values)
                DropdownMenuItem(value: k, child: Text(k.value)),
            ],
            onChanged: (v) => setState(() => kind = v ?? kind),
          ),
          const SizedBox(height: 12),
          TextField(
            controller: url,
            decoration: const InputDecoration(labelText: 'URL (link assets)'),
          ),
          const SizedBox(height: 12),
          TextField(
            controller: body,
            maxLines: 4,
            decoration: const InputDecoration(labelText: 'Body'),
          ),
          const SizedBox(height: 12),
          TextField(
            controller: tags,
            decoration: const InputDecoration(
              labelText: 'Tags',
              hintText: 'comma, separated',
            ),
          ),
        ],
      ),
    ),
    actions: [
      TextButton(onPressed: () => Navigator.pop(context), child: const Text('Cancel')),
      FilledButton(
        onPressed: () {
          final kindIn =
              MainAssetsInputKind.maybeValue(kind.value) ?? MainAssetsInputKind.note;
          Navigator.pop(
            context,
            MainAssetsInput(
              title: title.text.trim(),
              kind: kindIn,
              url: url.text.trim().isEmpty ? null : url.text.trim(),
              body: body.text.trim().isEmpty ? null : body.text.trim(),
              tags: tags.text
                  .split(',')
                  .map((t) => t.trim())
                  .where((t) => t.isNotEmpty)
                  .toList(),
            ),
          );
        },
        child: const Text('Save'),
      ),
    ],
  );
}
