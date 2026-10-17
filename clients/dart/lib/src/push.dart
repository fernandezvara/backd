import 'dart:async';

import 'auth.dart' show AuthEvent;
import 'client.dart';

/// What an app hands over to register a device.
final class DeviceInput {
  const DeviceInput({required this.platform, required this.token, this.name});

  /// `android`, `ios` or `web`.
  final String platform;

  /// The FCM registration token, the APNs device token or, for `web`, the
  /// subscription as JSON.
  final String token;

  /// A label to show the user, such as "Ana's Pixel".
  final String? name;

  Map<String, Object?> toJson() => {
        'platform': platform,
        'token': token,
        if (name != null) 'name': name,
      };
}

/// Push notifications (`client.push`): register the devices that should
/// receive the signed-in user's pushes. backd owns the registry, so a token
/// moves to whoever registers it last, and a device goes with the session
/// that registered it.
final class Push {
  Push(this.client) {
    // A device belongs to a session: once it ends, so does what this client
    // knew of it.
    client.auth.onAuthChange((change) {
      if (change.event != AuthEvent.signedIn) _deviceId = null;
    });
  }

  final BackdClient<Object?> client;

  String? _deviceId;

  /// The id of the device this client registered in this session, if it did.
  String? get deviceId => _deviceId;

  /// Registers this device, or refreshes it: call it at every launch and
  /// after signing in. The answer is the device without its token, and its
  /// id is kept for [unregister]. Needs a user session (an API key can't
  /// register devices).
  Future<Map<String, Object?>> register(DeviceInput device,
      {RequestOptions? options}) async {
    final res = await client.request(RequestInit(
      method: 'POST',
      path: const ['_auth', 'devices'],
      body: device.toJson(),
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    final data = Map<String, Object?>.from(res.data as Map);
    _deviceId = data['id'] as String?;
    return data;
  }

  /// The signed-in user's devices, most recently seen first.
  Future<List<Map<String, Object?>>> devices({RequestOptions? options}) async {
    final res = await client.request(RequestInit(
      method: 'GET',
      path: const ['_auth', 'devices'],
      headers: options?.headers,
      retry: options?.retry,
      cache: options?.cache ?? true,
    ));
    return [
      for (final d in (res.data as Map?)?['items'] as List? ?? const [])
        if (d is Map) Map<String, Object?>.from(d),
    ];
  }

  /// Removes a device: the one this client registered when [id] is not
  /// given (nothing happens, and it resolves `false`, when it registered
  /// none).
  Future<bool> unregister({String? id, RequestOptions? options}) async {
    final target = id ?? _deviceId;
    if (target == null) return false;
    await client.request(RequestInit(
      method: 'DELETE',
      path: ['_auth', 'devices', target],
      headers: options?.headers,
      retry: options?.retry,
    ));
    if (target == _deviceId) _deviceId = null;
    return true;
  }

  /// Keeps this device registered: now, if the user is signed in, and after
  /// every sign-in. [getToken] returns what to register, or null when the
  /// app has no token yet (permission not given, or none issued). Failures
  /// go to [onError]. It is what recovers a realm whose VAPID key was
  /// rotated, whose devices were removed.
  ///
  /// Returns a function that stops registering.
  void Function() registerOnLaunch(
    FutureOr<DeviceInput?> Function() getToken, {
    void Function(Object error)? onError,
  }) {
    var active = true;
    Future<void> run() async {
      try {
        final input = await getToken();
        if (active && input != null) await register(input);
      } catch (e) {
        onError?.call(e);
      }
    }

    final stop = client.auth.onAuthChange((change) {
      if (change.event == AuthEvent.signedIn) unawaited(run());
    });
    unawaited(client.auth.token().then((token) {
      if (token != null && active) return run();
    }));
    return () {
      active = false;
      stop();
    };
  }
}
