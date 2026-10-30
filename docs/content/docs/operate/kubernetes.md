---
title: "Kubernetes"
description: "The Helm chart: the workloads, the configuration baked into the image, the release pipeline, rollbacks, network policies and who may reach the admin host."
icon: "cloud-arrow-up"
weight: 313
toc: true
---

`deploy/kubernetes/backd` is a Helm chart for `backd` on any Kubernetes cluster; [Deploying on DigitalOcean](../digitalocean/) shows the whole path on one provider. The chart creates no infrastructure and no secret: the objects it reads (Secrets and a ConfigMap) are made by whatever builds the cluster, which for DigitalOcean is the Terraform in `deploy/terraform/digitalocean`.

## The workloads

It maps the [production reference](../production/) one to one:

| Deployment | Image | What it does | Reads the configuration |
|---|---|---|---|
| `backd` | your configuration image | the public API (no admin API, no change stream) | yes |
| `backd-realtime` | your configuration image | the only pods that read the change stream (realtime) and hold the trigger lease | yes |
| `backd-admin` | your configuration image | the admin API and interface | yes |
| `worker` | your configuration image | [async jobs](../../build/functions/jobs/) and cron | yes |
| `executor` | `ghcr.io/fernandezvara/backd-executor` | one Deno process per function call | no |
| `egress` | `ghcr.io/fernandezvara/backd` | the only way out for functions and push | no |
| `backd-provision` (Job) | your configuration image | `backd provision`, before an install, an upgrade or a rollback | yes |

Each is `replicas.<name>` in the values, and `replicas.default` is used by every component without a number of its own. A component with more than one pod gets a PodDisruptionBudget. Use one namespace per installation: the Services have fixed names.

## The configuration is the image

The configuration and the built functions are part of an immutable image, so every pod of a release runs exactly the same one (see [Deploying config](../deploying/)). The serving pods start in `PROVISION_MODE=verify` and refuse a database that differs from their configuration or was provisioned with another one. A release is a new image tag, `helm upgrade --set image.tag=<release>` is the rollout, and the provisioning Job runs first with the new image. Pods run with a read-only root filesystem, as non-root, with no capabilities, and the namespace enforces the `restricted` pod security standard.

**A rollback needs the Job too:** a pod whose configuration is not the one last provisioned refuses to start, so `helm rollback` runs the provisioning Job first, with the old image. Provisioning is idempotent and additive for collections. A change the old schema rejects (a field it forbids, already stored in documents) needs the data fixed before rolling back. During a rollout, a pod of the *old* release that restarts fails verification until the rollout completes.

## The release pipeline

`deploy/kubernetes/pipeline` holds a `Dockerfile`, an example GitHub Actions workflow and a guard. On a tag the workflow:

1. refuses a realm with `auth: disabled`;
2. builds the configuration image: the functions are bundled on the released executor image (it has the Deno this release runs functions with) and `backd config check` runs there and again in the final image, so an invalid configuration fails the build and nothing is pushed;
3. pushes it to the registry as `<registry>/<name>:<tag>`, one repository for every release;
4. runs `helm upgrade --install --atomic --wait`, in a protected `production` environment so a person can approve it;
5. prunes old tags (`registry-prune.sh`, never the running one; the registry's garbage collection is what frees the space).

{{< hint style="note" >}}
The guard against realms without authentication is a step, not a setting. If you are certain such a realm belongs in the installation, remove the step on purpose and say why in the commit. Nothing else checks it.
{{< /hint >}}

## Traffic in

The ingress controller is Traefik (ingress-nginx was retired in March 2026). It ends TLS with certificates cert-manager issues, redirects HTTP to HTTPS and sets HSTS. The public host routes `/v1/` only: `/_ui/`, `/healthz`, `/readyz` and `/metrics` are not reachable from outside. The limits are per client address, with the numbers of the Compose reference's nginx (`ingress.limits`): 20 requests a second with a burst of 40 for everything, 10 a minute with a burst of 5 for sign-up, login and the account flows, 2 a second for calling a function, 20 simultaneous requests, and a 1 MiB body except for files. Realtime streams go to the `backd-realtime` pods and have no limit on how long they last. The client's address reaches `backd` (the load balancer sends the PROXY protocol, and only the cluster's private network is trusted to say so), and `X-Forwarded-For` is never believed from a client.

### Who may reach the admin host

The admin host goes to `backd-admin` (the admin API and the interface at `/_ui/`) **from the allowed addresses only**. `ingress.admin.allowedCIDRs` is required on the first install: the chart refuses to render with an empty list, so the host is never opened to everyone. To change it without a release:

```sh
deploy/kubernetes/scripts/admin-allow.sh 203.0.113.7/32,198.51.100.0/24
deploy/kubernetes/scripts/admin-allow.sh --show
```

The script patches the Traefik Middleware and keeps the list in the ConfigMap `backd-admin-allow`, which the chart reads on every `helm upgrade` and which wins over the values (they only seed the first install). It refuses `0.0.0.0/0`, `::/0` and any `/0`, and an address with no prefix.

If your address changed and the admin host refuses you, you are not locked out of the cluster: the list concerns HTTP to that host only, and `kubectl` and Terraform use the Kubernetes and DigitalOcean APIs. Run the script with your cluster credentials, or reach the admin instance directly with `kubectl port-forward svc/backd-admin 8081:8080 -n backd`.

## Network policies

Nothing is allowed unless a policy says so. The ingress controller reaches the API, realtime and admin pods on port 8080 and nothing else reaches them; the executor reaches `backd`'s internal listener and egress and has no route to MongoDB, the object storage or the internet; MongoDB is reached by the `backd` pods, the worker and the provisioning Job only; and only egress (and the pods that need HTTPS, for the object storage and identity providers) reaches the internet, never a private address. The policies need a network plugin that enforces them; DigitalOcean's clusters have one.

## Verifying the files

`scripts/check-chart.sh` runs `helm lint`, renders the chart with every values file in `ci/`, validates the result with `kubeconform` and checks the properties above (security contexts, which workload gets which secret, where the trigger lease is, that a rollback re-provisions). `scripts/check-terraform.sh` runs `terraform fmt`, `validate` and offline `terraform test` with the provider mocked. Both run in CI.
