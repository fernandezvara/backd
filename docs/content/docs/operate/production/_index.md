---
title: "Production deployment"
description: "What every internet-facing installation needs, wherever it runs: TLS, rate limits, client addresses, a closed admin API, MongoDB with authentication and least privilege, isolated functions and files. Then how Docker Compose and Kubernetes do it, and where to host."
icon: "rocket-launch"
weight: 310
toc: true
---

`backd` deliberately leaves some protections to the infrastructure around it: TLS, request rate limits and sign-up spam, body size limits at the edge, and database security. They are the same wherever `backd` runs. This section separates *what* to put in place, on this page, from the *technology* that does it ([Docker Compose](docker-compose/) or [Kubernetes](kubernetes/)) and from the *place* it runs ([cloud providers](cloud-providers/)).

## What every installation needs

| Protection | What it means | Docker Compose | Kubernetes |
|---|---|---|---|
| **TLS** | `backd` serves plain HTTP; terminate TLS in front of it and redirect HTTP to HTTPS | [TLS](docker-compose/#tls) | [Traffic in](kubernetes/#traffic-in) |
| **Rate limits** | Limit requests per address, with a stricter zone for sign-up, password reset and verification | [Rate limits](docker-compose/#rate-limits) | [Traffic in](kubernetes/#traffic-in) |
| **Client addresses** | The proxy overwrites `X-Forwarded-For`; `TRUSTED_PROXIES` lists only your proxies | [Client addresses](docker-compose/#client-addresses) | [Traffic in](kubernetes/#traffic-in) |
| **Body size** | Cap request bodies at the edge | [Request body size](docker-compose/#request-body-size) | [Traffic in](kubernetes/#traffic-in) |
| **A closed admin API** | Public instances serve no admin API (`BACKD_ADMIN_API=false`); a separate internal instance does | [Admin API](docker-compose/#admin-api) | [Who may reach the admin host](kubernetes/#who-may-reach-the-admin-host) |
| **MongoDB** | Authentication and TLS, reachable only from `backd`, a user limited to the configured collections, `PROVISION_MODE=verify` | [MongoDB with authentication and TLS](docker-compose/#mongodb-with-authentication-and-tls), [Least-privilege provisioning](docker-compose/#least-privilege-provisioning) | [Network policies](kubernetes/#network-policies); [what a managed database allows](cloud-providers/digitalocean/#what-digitalocean-does-not-allow) |
| **Functions and files** | The executor reaches only `backd egress` and the internal listener; object storage sits on a network only `backd` and its worker share | [Functions](docker-compose/#functions-executor-egress-and-worker), [Files](docker-compose/#files-object-storage-on-its-own-network) | [Network policies](kubernetes/#network-policies) |
| **Secrets** | Random, long, kept out of the configuration and the images, rotated without downtime | [Administrators and API keys](docker-compose/#administrators-and-api-keys), [Rotating passwords](docker-compose/#rotating-passwords) | [Secrets on Kubernetes](kubernetes/#secrets-on-kubernetes) |

{{< hint style="warning" title="Never expose auth: disabled" >}}
A realm with `auth: disabled` has no authentication. It must never be reachable from the internet, on any platform. The [security model](../administration/security/) says how to check.
{{< /hint >}}

Before going public, go through the [hardening checklist](../checklist/), and before exposing a realm the [security checklist](../administration/security/#checklist-before-exposing-a-realm).

## Pick the technology, then the place

- **[Docker Compose](docker-compose/):** a tested reference deployment (`deploy/production`), end-to-end tested by CI. One host, or the model for your own.
- **[Kubernetes](kubernetes/):** a Helm chart, the configuration baked into the image, a release pipeline, and the secrets of an installation.
- **[Cloud providers](cloud-providers/):** where the infrastructure comes from: the Terraform and the walkthrough for a provider.
