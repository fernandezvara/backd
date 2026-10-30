---
title: "Secrets on Kubernetes"
description: "Which secrets a Kubernetes installation has, who creates them, where they are stored, how to rotate each one and what to back up."
icon: "key"
weight: 314
toc: true
---

An installation on [Kubernetes](../kubernetes/) has a handful of secrets. None is in the repository, none is in an image, and the Helm chart creates none: Terraform generates them, keeps them in its state and creates the Kubernetes Secrets the chart reads. This page is the full list.

| Secret | Created by | Stored in | Used by |
|---|---|---|---|
| `BACKD_SECRETS_KEY` | Terraform (`random_password`) | the state; Secret `backd-keys` | every `backd` process: it seals the realm secrets stored in MongoDB |
| `BACKD_EXECUTOR_TOKEN` | Terraform | the state; `backd-keys` | `backd`, the worker and the executor |
| `BACKD_CALLBACK_KEY` | Terraform | the state; `backd-keys` | `backd` and the worker (never the executor) |
| `BACKD_EGRESS_KEY` | Terraform | the state; `backd-keys` | the executor, egress and the worker |
| `BACKD_METRICS_TOKEN` | Terraform | the state; `backd-keys` | every process (`METRICS_TOKEN`) |
| `MONGO_URI` (user `backd`) | Terraform, from the database | the state; Secret `backd-mongo` | the serving pods and the worker |
| `MONGO_ADMIN_URI` (`doadmin`) | Terraform, from the database | the state; `backd-mongo` | the provisioning Job only |
| Registry pull credential | Terraform | the state; Secret `backd-registry` | the cluster, to pull the configuration image |
| Registry push credential | Terraform | the state; a CI secret | the pipeline |
| DNS token | you | the state; a Secret in cert-manager's namespace | cert-manager, for certificates |
| Spaces keys | Terraform | the state; **realm secrets in MongoDB** (`backd secret set`) | `backd`, for the files |
| The first administrator | `backd bootstrap` | MongoDB | you |

Each workload gets only the secrets it uses: the executor, which runs functions, never gets the database, the callback key or `BACKD_SECRETS_KEY`; egress only its own key; and only the provisioning Job sees the administrative database user. The Spaces keys are not environment variables of any pod: `backd` stores them encrypted under `BACKD_SECRETS_KEY` in MongoDB, as [realm secrets](../providers/storage/#the-access-keys).

{{< hint style="danger" title="The state is the safe, and the state bucket is its door" >}}
Terraform's state holds every generated secret, both database URIs and the Spaces keys. It lives in the private Space the bootstrap step makes, and anyone who can read that Space or the state key can read all of them. Give the key to as few people as possible. Likewise, a Kubernetes Secret is only base64: anyone who can `get secrets` in the namespace can read them, so grant that right to the people who run the installation and nobody else.
{{< /hint >}}

## Rotating

`terraform apply -replace=random_password.<name>` makes a new value and updates `backd-keys`. Pods read it when they start, so restart the workloads that use it (`kubectl rollout restart`, or the next release).

| Replace | Restart | Before |
|---|---|---|
| `executor_token` | `backd`, `backd-admin`, `backd-realtime`, the worker and the executor, together | nothing; calls to functions fail between the first and the last restart |
| `callback_key`, `egress_key`, `metrics_token` | every workload that has it | nothing |
| `secrets_key` | every `backd` workload | **`backd secret rotate-key`** (it re-encrypts the stored secrets under `BACKD_SECRETS_KEY` and `BACKD_SECRETS_NEW_KEY`); replacing the key without it makes every stored secret unreadable |
| the registry push credential | none | `-replace=digitalocean_container_registry_docker_credentials.push`, then update the CI secret |
| a Spaces key | none | `-replace` the key, then `backd secret set` the new one: no restart, the key is read within a minute |

## What to back up

**`BACKD_SECRETS_KEY`**, once, in a password manager: `kubectl get secret backd-keys -n backd -o jsonpath='{.data.BACKD_SECRETS_KEY}' | base64 -d`. The state holds it, but if the state is lost the stored realm secrets (the Spaces keys, function secrets, provider client secrets) are unrecoverable without it. The infrastructure survives a lost state, but Terraform can no longer manage it. Enable versioning on the state bucket (the bootstrap does) and keep the bootstrap's own small state file somewhere safe.
