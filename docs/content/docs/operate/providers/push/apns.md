---
title: "APNs: iOS"
description: "Set up Apple's push service for a realm: the key from the Apple Developer account, the identifiers, sandbox and production, and the egress host."
icon: "apple-logo"
weight: 586
toc: true
aliases: ["/docs/operate/providers/apns/"]
---

`ios.provider: apns` sends to iPhones and iPads directly through Apple's push service: HTTP/2, authenticated with a **token** signed by a key you create in your Apple Developer account. (Apps on the FCM SDK use [FCM](../fcm/) instead.)

## 1. The key

1. In the [Apple Developer account](https://developer.apple.com/account): *Certificates, Identifiers & Profiles → Keys → +*. Name it, tick **Apple Push Notifications service (APNs)**, and continue.
2. Download the `.p8` file: **it can be downloaded once**. Note the 10-character **Key ID** shown.
3. Note your **Team ID**: the 10 characters at the top right of the account's *Membership details*.
4. The **topic** is your app's bundle id (for example `com.acme.app`). The app's *App ID* must have the **Push Notifications** capability enabled.

## 2. The secret

```sh
backd secret set --realm acme --name APNS_KEY < AuthKey_AB12CD34EF.p8
```

The file is read as it is, newlines included.

## 3. realm.yaml

```yaml
push:
  ios:
    provider: apns
    key: secret:APNS_KEY
    key_id: "AB12CD34EF"
    team_id: "T123456789"
    topic: com.acme.app
    sandbox: false
```

## Sandbox and production

Apple has two environments with **separate device tokens**:

| `sandbox` | Host | For |
|---|---|---|
| `true` | `api.sandbox.push.apple.com` | builds run from Xcode (development provisioning) |
| `false` | `api.push.apple.com` | TestFlight and the App Store |

A token made in one is refused (`BadDeviceToken`) by the other. A realm that serves both kinds of build needs care: the guide's recommendation is `sandbox: true` only in development realms, and `false` in the production realm, where development builds simply don't get pushes.

## 4. The network

The worker needs `api.push.apple.com` (or the sandbox host), through the [egress proxy](../../../production/docker-compose/#functions-executor-egress-and-worker) of the deployment: it opens a CONNECT tunnel and speaks HTTP/2 inside it, which backd tests. Without a proxy it dials the host directly, public addresses only.

## What backd sends and does

For each device: `POST /3/device/<token>` with `apns-topic` (your topic), `apns-push-type` (`alert`, or `background` for a silent push), `apns-priority` (10 for `priority: high`, 5 otherwise and always for silent), `apns-expiration` (from `ttl`) and `apns-collapse-id` (from `group`). The body is `aps` (`alert`, `badge`, `sound`, `thread-id`) plus your `data` and `link` at the top level.

| APNs answers | Result |
|---|---|
| `200` | `sent` |
| `410` (`Unregistered`), `BadDeviceToken` | `invalid`: the device is removed |
| `403` with `InvalidProviderToken` / `ExpiredProviderToken` | the key was refused: the whole unit is tried again |
| `429`, `5xx` | `retry` |
| anything else (`PayloadTooLarge`, …) | `failed` |

The provider token is an ES256 JWT, reused for 50 minutes: Apple refuses a token refreshed more often than every 20 minutes, and one older than an hour.
