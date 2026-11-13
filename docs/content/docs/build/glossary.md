---
title: "Glossary"
description: "The words backd's documentation uses, in alphabetical order, one line each, with the page that explains each."
icon: "book-open"
weight: 60
toc: true
---

The terms newcomers meet first, each with one line and a link. When two terms are easy to mix up, the entry says how they differ.

## API key

A credential for a server-side service, with full access to the realm's data unless you give it [scopes](../auth/api-keys/). It is not a session: nobody signs in with it. See [API keys](../auth/api-keys/).

## Collection

A set of documents of one shape: a folder with `schema.json`, `collection.yaml` and optionally `indexes.json`. It is stored as one MongoDB collection. See [Data model](../configuration/config-dir/).

## Connection

An [organization](#organization)'s own sign-in settings for one [provider](#provider): the application the company registered with Google or Microsoft, a client secret and who may come in. See [company sign-in](../organizations/sign-in/).

## Cron schedule

A `schedule` on an [async function](#job) that makes it run by itself at set times. See [Cron](../functions/cron/).

## Database

A group of a realm's collections (`main`, say). It is stored as the MongoDB database `<realm>__<database>`. Not the same as a [realm](#realm). See [Data model](../configuration/config-dir/).

## Document

A JSON object in a collection: your fields plus the server-owned [`_meta`](#_meta). See [Documents](../api/documents/).

## Egress

The proxy (`backd-server egress`) that is the only way a [function](#function) reaches the outside network, and only the hosts the function declares. See [Outbound network](../functions/network/).

## Executor

The process (`backd-server executor`) that runs each function call in its own Deno process and stops it at its deadline. It never sees the config directory, MongoDB or the signing keys. See [Executor, workers and egress](../../operate/functions-runtime/).

## Function

A small TypeScript program `backd` runs in a sandbox, for the logic a client must not do. It can be called, run as a [job](#job), by a [cron schedule](#cron-schedule), as a [webhook](#webhook) or by a [trigger](#trigger). See [Functions](../functions/).

## Job

The record of an `async` function call: the call answers `202` at once and a [worker](#worker) runs it later. See [Jobs](../functions/jobs/).

## `_meta`

The part of every document that the server owns: timestamps, `version`, and in a realm with auth the owner and who created and last changed it. Requests cannot set it. See [Documents](../api/documents/).

## Method

One step of a [strategy](#strategy): `password`, `magic_link`, `sso`, a [provider](#provider)'s name, or `totp` after another method. The first method says who is signing in. See [Authentication strategies](../auth/strategies/).

## Organization

A group of users that documents can belong to and rules can ask about: a company, a workspace, a club. Not the same as a [realm](#realm), which is the whole application. See [Organizations](../organizations/).

## Passkey

A key pair made on a person's device, password manager or security key for the realm's domain: backd keeps the public key, and the person signs in with a fingerprint, face or PIN instead of a password. A [method](#method) of a strategy. See [Passkeys](../auth/passkeys/).

## Provider

An outside sign-in service, such as Google, Microsoft or Apple, declared in the realm's `providers:`. For a company's own use of one, see [connection](#connection). See [Sign in with a provider](../auth/providers/).

## Realm

The authority a set of data shares: its users, roles, sessions, API keys, organizations, email templates and secrets. A realm can be a whole application or several applications that share one sign-in. See [Getting started](../../getting-started/) and [Realm settings](../configuration/realm/).

## Relying party

The website a passkey is made for, named by `auth.passkeys.rp_id` (a domain such as `example.com`). A passkey works only on pages within it, and changing it loses every registered passkey. See [Passkeys](../auth/passkeys/#turning-them-on).

## Role

A name a user holds across the whole realm (`admin`, `curator`), listed in `realm.yaml`, which rules and functions can check. Not the same as a [team role](#team-role). See [Realm settings](../configuration/realm/#roles).

## Rule

An expression in a collection's `rules:` that decides, per document, whether a read or write is allowed. See [Access rules](../auth/rules/).

## Scope

A restriction on an [API key](#api-key): the operations it may use and, in a realm with organizations, the one organization it counts. See [API keys](../auth/api-keys/).

## Session

What signing in creates: a token for one user (and, in a realm with organizations, one organization), with an idle timeout and a maximum lifetime. See [Sign-up, login and sessions](../auth/sessions/).

## Strategy

A named, ordered list of [methods](#method) a person completes to sign in, such as `admin_password: [password, totp]`; the app says which one it uses, and a role may accept only some. See [Authentication strategies](../auth/strategies/).

## Team

A group of an organization's members that carries organization roles to everyone in it. See [Team roles](../organizations/team-roles/).

## Team role

A role held within one team by a member of the organization; it means nothing outside that team. Not the same as a [role](#role). See [Team roles](../organizations/team-roles/).

## Trigger

A function that runs when documents of a collection are created, updated or removed, whoever changed them. It is declared in `collection.yaml`. See [Triggers](../functions/triggers/).

## Webhook

A function with `mode: webhook` that answers a callback from another service (Stripe, GitHub, …) itself. Anyone on the internet can call it, so it must check the sender's signature. See [Webhooks](../functions/webhooks/).

## Worker

The process (`backd-server worker`, or `backd-server serve --with-worker`) that runs async [jobs](#job) and creates the runs of [cron schedules](#cron-schedule). Without one, a job stays `queued`. See [Running the worker](../../operate/functions-runtime/#running-the-worker).

## How the processes fit

For how the processes fit together (the API, the executor, the worker, egress, MongoDB and storage), see [how the pieces fit](../../operate/#how-the-pieces-fit).
