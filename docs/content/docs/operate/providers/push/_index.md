---
title: "Push providers"
description: "The senders a realm uses to reach devices: FCM, APNs and Web Push, plus the capture provider for development."
icon: "bell"
weight: 580
toc: true
---

A realm sends push notifications through the providers named in its `push:` section of `realm.yaml` — one per platform (`android`, `ios`, `web`), each with its credentials kept as realm secrets (`secret:NAME`), never in the file. The [Push notifications](../../../build/push/) pages cover the feature itself: devices, templates, sends and records. These pages cover each provider's accounts, keys and network needs:

| Provider | For |
|---|---|
| [FCM](fcm/) | Android, and iOS apps on the FCM SDK |
| [APNs](apns/) | iPhones and iPads directly |
| [Web Push](web-push/) | browsers, with a VAPID key pair `backd` generates |
| [Local development](local/) | the `capture` provider of the local stacks: no vendor account, deliveries inspected in the Mailbox |

Sends go out from the **worker**, not the API process: a deployment that queues pushes needs `backd worker` (or `serve --with-worker`) running, and a route to the provider's hosts — through the egress proxy when the deployment has one.
