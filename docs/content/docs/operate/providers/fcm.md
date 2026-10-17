---
title: "FCM: Android, and iOS apps on the FCM SDK"
description: "Set up Firebase Cloud Messaging for a realm: the Firebase console steps, the service account, the realm.yaml keys and the egress hosts."
icon: "fire-simple"
weight: 585
toc: true
---

FCM is the provider for **Android**, and for **iOS apps that use the FCM SDK** (`ios.provider: fcm`). backd sends through FCM's HTTP v1 API from the worker, with an OAuth2 access token it mints from a **service account**.

## 1. A Firebase project

1. Open the [Firebase console](https://console.firebase.google.com) and create a project, or pick the one your app uses.
2. **Android:** add an Android app with your application id (the package name). Download `google-services.json` for the app's build.
3. **iOS over FCM:** add an iOS app with the bundle id, download `GoogleService-Info.plist`, and upload an **APNs authentication key** under *Project settings → Cloud Messaging → Apple app configuration* (see [APNs](../apns/#1-the-key) for where it comes from). That key lives in Firebase: backd never sees it.
4. In the linked Google Cloud project, enable the **Firebase Cloud Messaging API (V1)** (*APIs & Services → Library*). The legacy API is not used.

## 2. A service account

1. *Project settings → Service accounts → Generate new private key.* A JSON file downloads: it holds `project_id`, `client_email` and `private_key`.
2. Store it as a realm secret, from the file:

```sh
backd secret set --realm acme --name FCM_SERVICE_ACCOUNT < service-account.json
```

The value is read from standard input, newlines included, and encrypted with the instance's `BACKD_SECRETS_KEY`. Delete the downloaded file afterwards.

## 3. realm.yaml

```yaml
push:
  android:
    provider: fcm
    credentials: secret:FCM_SERVICE_ACCOUNT
  ios:
    provider: fcm                      # only for apps on the FCM SDK; APNs directly is `apns`
    credentials: secret:FCM_SERVICE_ACCOUNT
```

`web` can use `provider: fcm` too, for tokens from the FCM web SDK (not Push API subscriptions): the app registers the FCM token as the device's `token`.

## 4. The network

Requests go through the [egress proxy](../../production/#functions-executor-egress-and-worker) of the deployment, with a token the worker signs for each delivery unit naming only these hosts:

| Host | For |
|---|---|
| `oauth2.googleapis.com` | the access token (`token_uri` of the service account) |
| `fcm.googleapis.com` | the messages |

Without a proxy (a single-process development setup) the worker dials them directly, and only public addresses. The worker needs `EXECUTOR_PROXY` and `BACKD_EGRESS_KEY` to use the proxy, like the executor.

## What backd does with the answers

| FCM answers | Result |
|---|---|
| `200` | `sent` |
| `404` / `UNREGISTERED` | `invalid`: the device is removed |
| `400 INVALID_ARGUMENT` about the registration token | `invalid` |
| `429`, `5xx`, `UNAVAILABLE` | `retry`: only these devices, after the backoff |
| `401`, `403` | the credentials were refused: the whole unit is tried again |
| anything else | `failed` |

The access token is cached until a minute before it expires, so a busy worker asks for one about once an hour.

{{< hint style="note" >}}
A unit that fails as a whole after some devices were already accepted is sent again to every device still pending, so a device may receive the push twice. Use `group` (FCM's `collapse_key`) to make a repeat replace the first.
{{< /hint >}}
