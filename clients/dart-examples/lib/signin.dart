import 'dart:async';

import 'package:backd/backd.dart';
import 'package:flutter/material.dart';

import 'main.dart';

/// Sign-in: email + password, then whatever step the answer asks for —
/// a second factor's code, a first-factor enrollment (the role demands one),
/// or consents to accept. A magic link is the other way in.
///
/// A person signs in to a team (chapter 20): the page asks for it, then offers
/// only what that team allows, says the same thing for every failure, helps
/// someone who forgot the team's short name, and lets a new person start a
/// team of their own (with the operator's invitation, since signup is closed).
class SignInScreen extends StatefulWidget {
  const SignInScreen({super.key, required this.state});

  final AppState state;

  @override
  State<SignInScreen> createState() => _SignInScreenState();
}

class _SignInScreenState extends State<SignInScreen> {
  final email = TextEditingController();
  final password = TextEditingController();
  final code = TextEditingController();
  final org = TextEditingController();
  final teamName = TextEditingController();
  final invitation = TextEditingController();

  /// The step the last answer asked for: 'password' (none yet), 'totp',
  /// 'recovery_code', 'enroll', 'magic' (link code), or '' while working.
  var step = 'password';
  String? error;
  MfaChallenge? mfa;
  EnrollmentChallenge? enrollment;
  MfaSetup? setup;
  ConsentChallenge? consent;
  String? magicRequestId;
  String? notice;

  /// What the typed team allows (asked as it is typed; an unknown team answers
  /// like any other, so this never says a team is wrong).
  var methods = const ['password', 'magic_link'];
  Timer? asking;

  @override
  void initState() {
    super.initState();
    // The team last used on this device, ready to change.
    org.text = widget.state.team ?? '';
    if (org.text.isNotEmpty) askMethods();
  }

  @override
  void dispose() {
    asking?.cancel();
    super.dispose();
  }

  /// The short name typed, as the API reads it; null for the operator.
  String? get teamRef {
    final t = org.text.trim().toLowerCase();
    return t.isEmpty ? null : t;
  }

  /// The client of the team typed: its session lands in its own slot.
  Auth get auth {
    final ref = teamRef;
    return ref == null
        ? widget.state.root.auth
        : widget.state.root.org(ref).auth;
  }

  void teamChanged(String _) {
    asking?.cancel();
    asking = Timer(const Duration(milliseconds: 400), askMethods);
  }

  Future<void> askMethods() async {
    final ref = teamRef;
    try {
      final offer = ref == null
          ? (providers: <String>[], methods: const ['password', 'magic_link'])
          : await widget.state.root.auth.options(org: ref);
      if (mounted) {
        setState(
          () => methods = offer.methods.isEmpty
              ? const ['password']
              : offer.methods,
        );
      }
    } on BackdException {
      if (mounted) setState(() => methods = const ['password', 'magic_link']);
    }
  }

  /// Walks a sign-in answer to its next step; a Session finishes it.
  Future<void> handle(SignInResult result) async {
    switch (result) {
      case Session():
        await widget.state.signedIn(teamRef);
      case MfaChallenge c:
        setState(() {
          mfa = c;
          step = c.methods.contains('totp')
              ? 'totp'
              : (c.methods.isNotEmpty ? c.methods.first : 'totp');
          error = null;
        });
      case EnrollmentChallenge c:
        final s = await auth.mfa.setup(challenge: c.challenge);
        setState(() {
          enrollment = c;
          setup = s;
          step = 'enroll';
          error = null;
        });
      case ConsentChallenge c:
        setState(() {
          consent = c;
          step = 'consent';
          error = null;
        });
      default:
        setState(() => error = 'unexpected sign-in answer: $result');
    }
  }

  Future<void> run(Future<void> Function() work) async {
    setState(() {
      error = null;
      notice = null;
    });
    try {
      await work();
    } on BackdException catch (e) {
      setState(
        () => error = e.code == 'invalid_credentials'
            // The same words for a wrong team, address or password: the API doesn't say which.
            ? "We couldn't sign you in. Check the team, email and password."
            : e.code == 'slug_taken'
            ? 'That short name is taken: choose another.'
            : '${e.code}: ${e.message}',
      );
    } catch (e) {
      setState(() => error = '$e');
    }
  }

  Future<void> submitPassword() => run(() async {
    await handle(await auth.signIn(email.text.trim(), password.text));
  });

  Future<void> submitCode() => run(() async {
    final c = mfa;
    if (c == null) return;
    await handle(await auth.verifyMfa(c.challenge, step, code.text.trim()));
  });

  Future<void> confirmEnrollment() => run(() async {
    final s = setup, e = enrollment;
    if (s == null || e == null) return;
    final res = await auth.mfa.confirm(
      setupId: s.setupId,
      code: code.text.trim(),
      challenge: e.challenge,
    );
    if (res.consent != null) {
      setState(() {
        consent = res.consent;
        step = 'consent';
      });
    } else if (res.session != null) {
      await handle(res.session!);
    }
    if (res.recoveryCodes.isNotEmpty && mounted) {
      await showDialog<void>(
        context: context,
        builder: (c) => AlertDialog(
          title: const Text('Recovery codes'),
          content: Text(
            'Shown once — keep them somewhere safe:\n\n${res.recoveryCodes.join('\n')}',
          ),
          actions: [
            FilledButton(
              onPressed: () => Navigator.pop(c),
              child: const Text('Done'),
            ),
          ],
        ),
      );
    }
  });

  Future<void> requestLink() => run(() async {
    final res = await auth.magicLink.request(email.text.trim());
    setState(() {
      magicRequestId = res.requestId;
      step = 'magic';
    });
  });

  Future<void> submitMagicCode() => run(() async {
    await handle(await auth.magicLink.verifyCode(code.text.trim()));
  });

  /// "Which teams am I in?": the answer goes to the mailbox of the address and
  /// tells this page nothing.
  Future<void> forgotTeam() => run(() async {
    if (email.text.trim().isEmpty) {
      setState(
        () => error = 'Type your email first: the links are sent there.',
      );
      return;
    }
    await widget.state.root.auth.findOrgs(email.text.trim());
    setState(
      () => notice =
          'If that address belongs to teams, an email with the links is on its way.',
    );
  });

  /// The short name suggested by a team's name: "Acme Corp" is `acme-corp`.
  String get suggestedSlug => teamName.text
      .trim()
      .toLowerCase()
      .replaceAll(RegExp(r'[^a-z0-9]+'), '-')
      .replaceAll(RegExp(r'^-|-$'), '');

  /// Starting a team: the account, the team and the session in one call. Signup
  /// is invite-only, so the operator's invitation comes with it.
  Future<void> createTeam() => run(() async {
    final slug = teamRef ?? suggestedSlug;
    final current = await widget.state.root.consents.current();
    final accepted = {
      for (final c in current)
        '${c['name']}': '${(c['current'] as Map)['version']}',
    };
    await widget.state.root.auth.signUpWithOrg(
      email.text.trim(),
      password.text,
      name: teamName.text.trim(),
      slug: slug,
      invitation: invitation.text.trim().isEmpty
          ? null
          : invitation.text.trim(),
      consents: accepted,
    );
    await widget.state.signedIn(slug);
  });

  Future<void> acceptConsents() => run(() async {
    final c = consent;
    if (c == null) return;
    await handle(
      await auth.acceptConsents(c.challenge, {
        for (final p in c.consents) p.name: p.version,
      }),
    );
  });

  @override
  Widget build(BuildContext context) => Scaffold(
    body: Center(
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 380),
        child: ListView(
          shrinkWrap: true,
          children: [
            Text('Shelf', style: Theme.of(context).textTheme.displayMedium),
            const SizedBox(height: 8),
            Text('Sign in to ${widget.state.url}'),
            const SizedBox(height: 24),
            if (widget.state.adding && widget.state.team != null)
              TextButton(
                onPressed: widget.state.cancelAdding,
                child: Text('Back to ${widget.state.team}'),
              ),
            if (step == 'password' || step == 'magic' || step == 'create') ...[
              TextField(
                controller: email,
                decoration: const InputDecoration(
                  labelText: 'Email',
                  border: OutlineInputBorder(),
                ),
                keyboardType: TextInputType.emailAddress,
              ),
              const SizedBox(height: 12),
              TextField(
                controller: org,
                decoration: InputDecoration(
                  labelText: step == 'create' ? 'Short name' : 'Team',
                  hintText: step == 'create'
                      ? 'suggested from the name'
                      : "your team's short name (empty: operator)",
                  border: const OutlineInputBorder(),
                ),
                onChanged: teamChanged,
              ),
              const SizedBox(height: 12),
            ],
            if (step == 'password' || step == 'create') ..._passwordFields(),
            if (step == 'password') ...[
              if (methods.contains('password'))
                FilledButton(
                  onPressed: submitPassword,
                  child: const Text('Sign in'),
                ),
              if (methods.contains('magic_link'))
                TextButton(
                  onPressed: requestLink,
                  child: const Text('Email me a sign-in link instead'),
                ),
              TextButton(
                onPressed: forgotTeam,
                child: const Text('Forgot your team?'),
              ),
              TextButton(
                onPressed: () => setState(() {
                  step = 'create';
                  error = notice = null;
                }),
                child: const Text('Start a team of your own'),
              ),
            ],
            if (step == 'create') ...[
              TextField(
                controller: teamName,
                decoration: const InputDecoration(
                  labelText: 'Team name',
                  border: OutlineInputBorder(),
                ),
                onChanged: (_) => setState(() {}),
              ),
              const SizedBox(height: 12),
              TextField(
                controller: invitation,
                decoration: const InputDecoration(
                  labelText: "Operator's invitation",
                  border: OutlineInputBorder(),
                ),
              ),
              const SizedBox(height: 12),
              Text('Short name: ${teamRef ?? suggestedSlug}'),
              const SizedBox(height: 12),
              FilledButton(
                onPressed: createTeam,
                child: const Text('Start the team'),
              ),
              TextButton(
                onPressed: () => setState(() {
                  step = 'password';
                  error = null;
                }),
                child: const Text('I have a team already'),
              ),
            ],
            if (step == 'magic') ...[
              const Text(
                'A link and a code are on their way. The link opens in a browser; the code finishes here.',
              ),
              const SizedBox(height: 12),
              codeField('Code from the email', submitMagicCode),
            ],
            if (step == 'totp' || step == 'recovery_code') ...[
              Text(
                step == 'totp'
                    ? 'The code of the authenticator app:'
                    : 'A recovery code:',
              ),
              const SizedBox(height: 12),
              codeField('Code', submitCode),
              if (mfa != null && mfa!.methods.length > 1)
                Wrap(
                  spacing: 8,
                  children: [
                    for (final m in mfa!.methods)
                      ChoiceChip(
                        label: Text(m),
                        selected: step == m,
                        onSelected: (_) => setState(() => step = m),
                      ),
                  ],
                ),
            ],
            if (step == 'enroll') ...[
              const Text(
                'Your role asks for a second factor. Add this secret to an authenticator app, then type its first code:',
              ),
              const SizedBox(height: 8),
              SelectableText(
                setup?.secret ?? '',
                style: Theme.of(context).textTheme.titleMedium,
              ),
              const SizedBox(height: 12),
              codeField('First code', confirmEnrollment),
            ],
            if (step == 'consent') ...[
              const Text('To sign in, accept:'),
              const SizedBox(height: 8),
              for (final p in consent?.consents ?? <PendingConsent>[])
                ListTile(
                  dense: true,
                  leading: const Icon(Icons.check_circle_outline),
                  title: Text(p.name),
                  subtitle: Text('version ${p.version}'),
                ),
              const SizedBox(height: 12),
              FilledButton(
                onPressed: acceptConsents,
                child: const Text('Accept all and continue'),
              ),
            ],
            if (notice != null) ...[const SizedBox(height: 12), Text(notice!)],
            if (error != null) ...[
              const SizedBox(height: 12),
              Text(
                error!,
                style: TextStyle(color: Theme.of(context).colorScheme.error),
              ),
            ],
          ],
        ),
      ),
    ),
  );

  List<Widget> _passwordFields() => [
    TextField(
      controller: password,
      decoration: const InputDecoration(
        labelText: 'Password',
        border: OutlineInputBorder(),
      ),
      obscureText: true,
      onSubmitted: (_) => step == 'create' ? createTeam() : submitPassword(),
    ),
    const SizedBox(height: 12),
  ];

  Widget codeField(String label, Future<void> Function() submit) => Column(
    crossAxisAlignment: CrossAxisAlignment.stretch,
    children: [
      TextField(
        controller: code,
        decoration: InputDecoration(
          labelText: label,
          border: const OutlineInputBorder(),
        ),
        onSubmitted: (_) => submit(),
      ),
      const SizedBox(height: 12),
      FilledButton(onPressed: submit, child: const Text('Continue')),
    ],
  );
}
