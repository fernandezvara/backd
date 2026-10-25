import 'package:backd/backd.dart';
import 'package:flutter/material.dart';

import 'main.dart';

/// Sign-in: email + password, then whatever step the answer asks for —
/// a second factor's code, a first-factor enrollment (the role demands one),
/// or consents to accept. A magic link is the other way in.
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

  /// The step the last answer asked for: 'password' (none yet), 'totp',
  /// 'recovery_code', 'enroll', 'magic' (link code), or '' while working.
  var step = 'password';
  String? error;
  MfaChallenge? mfa;
  EnrollmentChallenge? enrollment;
  MfaSetup? setup;
  ConsentChallenge? consent;
  String? magicRequestId;

  Auth get auth => widget.state.client.auth;

  /// Walks a sign-in answer to its next step; a Session finishes it.
  Future<void> handle(SignInResult result) async {
    switch (result) {
      case Session():
        await widget.state.signedIn();
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
    setState(() => error = null);
    try {
      await work();
    } on BackdException catch (e) {
      setState(() => error = '${e.code}: ${e.message}');
    } catch (e) {
      setState(() => error = '$e');
    }
  }

  Future<void> submitPassword() => run(() async {
    await handle(
      await auth.signIn(
        email.text.trim(),
        password.text,
        org: org.text.trim().isEmpty ? null : org.text.trim(),
      ),
    );
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
            FilledButton(onPressed: () => Navigator.pop(c), child: const Text('Done')),
          ],
        ),
      );
    }
  });

  Future<void> requestLink() => run(() async {
    final res = await auth.magicLink.request(
      email.text.trim(),
      org: org.text.trim().isEmpty ? null : org.text.trim(),
    );
    setState(() {
      magicRequestId = res.requestId;
      step = 'magic';
    });
  });

  Future<void> submitMagicCode() => run(() async {
    await handle(await auth.magicLink.verifyCode(code.text.trim()));
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
            if (step == 'password' || step == 'magic') ...[
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
                decoration: const InputDecoration(
                  labelText: 'Team',
                  hintText: "your team's short name (empty: operator)",
                  border: OutlineInputBorder(),
                ),
              ),
              const SizedBox(height: 12),
            ],
            if (step == 'password') ...[
              TextField(
                controller: password,
                decoration: const InputDecoration(
                  labelText: 'Password',
                  border: OutlineInputBorder(),
                ),
                obscureText: true,
                onSubmitted: (_) => submitPassword(),
              ),
              const SizedBox(height: 12),
              FilledButton(onPressed: submitPassword, child: const Text('Sign in')),
              TextButton(
                onPressed: requestLink,
                child: const Text('Email me a sign-in link instead'),
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
            if (error != null) ...[
              const SizedBox(height: 12),
              Text(error!, style: TextStyle(color: Theme.of(context).colorScheme.error)),
            ],
          ],
        ),
      ),
    ),
  );

  Widget codeField(String label, Future<void> Function() submit) => Column(
    crossAxisAlignment: CrossAxisAlignment.stretch,
    children: [
      TextField(
        controller: code,
        decoration: InputDecoration(labelText: label, border: const OutlineInputBorder()),
        onSubmitted: (_) => submit(),
      ),
      const SizedBox(height: 12),
      FilledButton(onPressed: submit, child: const Text('Continue')),
    ],
  );
}
