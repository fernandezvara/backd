---
title: "Prove which domains an organization owns"
description: "Verified domains: an organization proves it controls an email domain with a DNS record, so features that rely on \"these addresses are ours\" can trust it."
icon: "shield"
weight: 530
toc: true
---

A company can say its people have addresses on `acme.example`, but nothing proves the company owns the domain. A **verified domain** is that proof: the organization publishes a DNS record that backd gives it, and backd checks it. Other features then rely on the domain being the organization's: an [organization sign-in](../sign-in/) can [link an existing account](#sign-in-through-a-verified-domain) on it, an application can [find the organization from an address](#find-an-organization-from-an-address), and the connection's `email_domains` may only name proved domains. On its own, a verified domain changes nothing for anybody.

## Turn it on

It is off unless `realm.yaml` has the section, and a realm that doesn't answers `404` to the whole surface. A role needs `manage: domains` to claim, prove, change and release domains:

```yaml
organizations:
  roles:
    owner: { manage: [org, members, roles, teams, domains] }
  domains:
    enabled: true            # default once the section is there
    max_per_org: 10          # claims one organization may have, verified or not (1-100)
    recheck: 7d              # how often a verified domain is checked again (at least 1h)
    lapse_after: 3           # failed checks in a row before a domain lapses (1-20)
    blocked: [example-free.test]   # more domains nobody may claim
    resolvers: []            # DNS servers to ask, as host:port; empty: the server's own resolver
```

With no `resolvers`, backd asks the resolver of the machine it runs on; the proof is only as trustworthy as that answer, so a production setup names resolvers it trusts. Free-mail providers (Gmail, Outlook, Yahoo, iCloud, Proton…), public suffixes (`com`, `co.uk`) and the `blocked` list can't be claimed.

## Prove a domain

1. **Claim it.** `POST /_orgs/{org}/domains` with `{"domain": "acme.example"}` (a manager, with a fresh re-authentication), or `backd orgs domain-add --id <org> --domain acme.example`. The domain is written in lower case and, if international, in punycode. The answer holds the record:

   ```json
   { "domain": "acme.example", "state": "pending",
     "record": { "type": "TXT", "name": "_backd-challenge.acme.example", "value": "backd-verify=7Qm2c1xJg9vKf0aTz3LwPA" } }
   ```

2. **Publish the record** in the domain's DNS.
3. **Check it.** `POST /_orgs/{org}/domains/{domain}/verify`, or `backd orgs domain-verify`, looks for exactly that value. When it is there the claim is `verified`; when it is not, the answer says why: `record_not_found`, `mismatch` (other values are there) or `resolver_error`. At most six checks an hour per domain. backd also checks by itself: a pending claim after one, five, fifteen minutes, an hour, six hours and a day, and then it waits for you.

A verified domain is checked again every `recheck`. If the record is gone for `lapse_after` checks in a row the domain **lapses** (about three weeks with the defaults), the managers get an email, and whatever relies on the domain stops relying on it. Publish the record again and the next check brings it back, unless another organization proved the domain meanwhile.

## One organization per domain

A domain is verified for **one organization per realm**. Several may be waiting for it at once, and the first whose check passes holds it; the others get `409 domain_taken` until it is released (the holder removes the claim, its organization is deleted, or the domain lapses). The match is exact: `acme.example` does not cover `eng.acme.example`, which is proved on its own. A realm administrator can release any claim (`backd orgs domain-remove`, `DELETE /_admin/orgs/{org}/domains/{domain}`), which is how a dispute is settled.

## Sign in through a verified domain

An organization's [connection](../sign-in/) never links an existing account on its provider's word alone: the company's managers control that provider, so what it says about an address is not proof the address is theirs. A verified domain is the proof, so on it a sign-in **does** link, when all of this holds:

1. the person signs in through a connection of the organization (which already restricts who may come in to its Workspace or tenant);
2. the provider vouches for the address: Google says it is verified, and a Microsoft tenant's address is on the domain of its account's name, a domain the tenant verified;
3. the address's domain is a **verified** domain of the organization (not pending, not lapsed) with `link_accounts` on;
4. an account has that address and **its address is verified in backd** (an address nobody confirmed is not enough);
5. that account has no identity of this connection yet.

The identity is then added to the account, the person joins the organization as any person who signs in through the connection for the first time, `org.sso.linked` is audited, and **the account's owner gets an email** saying that company's sign-in was linked, so a hostile identity provider is seen. If any condition fails the answer is the usual `account_exists` and the person links explicitly. A session opened through a connection cannot change the account's credentials, second factor, email or API keys, or erase or export it: the link gives access to the organization's data, not to the person's other organizations or to the account itself. A domain that lapses stops linking at once; the identities already linked stay.

Switch it off for a domain with `link_accounts: false`: the proof stays, and nothing links.

`email_domains` of a connection (the extra restriction on the addresses it admits) can only name verified domains of the organization once the realm turns `organizations.domains` on. A list the connection already had is kept when the feature is turned on, and any domain added to it must be proved.

## Find an organization from an address

An application that asks for an email address first can send the person to their company's sign-in:

```
POST /v1/{realm}/_auth/orgs/discover
{ "email": "ana@acme.example" }
→ { "items": [ { "org": "cr3s0k5v1d9u2n8e7a0g", "name": "Acme", "providers": ["microsoft"] } ] }
```

The application then starts `/_auth/orgs/{org}/oauth/{provider}/start` (see [the sign-in experience](../sign-in-experience/)). It answers only for a domain that is **verified**, whose claim is `discoverable` (the organization's choice, off by default, because it tells the world the company uses your app) and whose organization has a usable connection. Every other case, an unknown address, a domain nobody proved, one that is not discoverable, an organization with no working connection, answers the same, `{"items": []}`, with the same status and shape, and it never says whether an account or a member exists. At most 10 requests an hour per address and 60 a minute per client address.

## What a claim carries

| Field | Meaning |
|---|---|
| `state` | `pending`, `verified` or `lapsed` |
| `discoverable` | The organization lets the domain be used to find it from an email address. Default `false` |
| `link_accounts` | A sign-in through the organization's connection on this domain may link an existing account. Default `true` |
| `record` | The DNS record to publish |

Change the two switches with `PATCH /_orgs/{org}/domains/{domain}`, or in the organization's page of the admin UI, whose *Domains* section lists the claims with the record to publish, checks a pending one now and releases it.

## Good to know

{{< hint style="note" >}}
Domain claims travel with `--include auth` in an [export](../../../operate/administration/export-import/), but **as pending**: what was proved in one place proves nothing in another. Anonymized packages carry none.
{{< /hint >}}

- Everything is audited: `org.domain.added`, `org.domain.verified`, `org.domain.lapsed`, `org.domain.removed`, `org.domain.settings`.
- The only outbound traffic is the DNS lookup of `_backd-challenge.<domain>`: three seconds at most, ten answers of at most 255 bytes.
- A deleted organization's claims go with its cleanup, which frees its verified domains.
- DNSSEC is not required. If the DNS answer can be forged, so can the proof: use resolvers you trust.
