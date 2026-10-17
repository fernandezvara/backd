import 'client.dart';

/// Consents the realm asks for: `client.consents`. The texts are public (a
/// person reads them before having an account); the rest needs a session.
/// See `client.auth.signup` and `client.auth.acceptConsents` for accepting
/// at sign-up and at sign-in.
final class Consents {
  Consents(this.client);

  final BackdClient<Object?> client;

  /// The consents the realm asks for and the version in force of each.
  Future<List<Map<String, Object?>>> current({RequestOptions? options}) async {
    final res = await client.request(RequestInit(
      method: 'GET',
      path: const ['_consents'],
      auth: false,
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return [
      for (final c in (res.data as Map?)?['items'] as List? ?? const [])
        if (c is Map) Map<String, Object?>.from(c),
    ];
  }

  /// The exact text of a version, in the best of the realm's languages for
  /// [locale].
  Future<Map<String, Object?>> text(
    String name,
    String version, {
    String? locale,
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'GET',
      path: ['_consents', name, version],
      query: {'locale': locale},
      auth: false,
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }

  /// What the signed-in user holds, what they still have to accept and
  /// their history.
  Future<Map<String, Object?>> state({RequestOptions? options}) async {
    final res = await client.request(RequestInit(
      method: 'GET',
      path: const ['_auth', 'consents'],
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }

  /// The signed-in user accepts consents: `{'terms': '2026-10',
  /// 'marketing': '1'}`. It works for a user who is refused with
  /// `consent_required` because a `reaccept_by` date passed, which is how
  /// they get back in.
  Future<Map<String, Object?>> accept(
    Map<String, String> consents, {
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'POST',
      path: const ['_auth', 'consents'],
      body: {'consents': consents},
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }

  /// Withdraws an optional consent. A required one is a [ConflictError]
  /// with the code `consent_required_for_account`: the way out is deleting
  /// the account.
  Future<Map<String, Object?>> withdraw(
    String name, {
    RequestOptions? options,
  }) async {
    final res = await client.request(RequestInit(
      method: 'DELETE',
      path: ['_auth', 'consents', name],
      headers: options?.headers,
      retry: options?.retry,
    ));
    return Map<String, Object?>.from(res.data as Map);
  }
}
