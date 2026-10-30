# backd on Kubernetes

The Helm chart of backd (`backd/`), the scripts around it, and the design behind them: [`design/kubernetes.md`](../../design/kubernetes.md). The infrastructure it runs on is made by Terraform ([`deploy/terraform/digitalocean`](../terraform/digitalocean/README.md) today; `deploy/terraform/<provider>` for others), which also creates the Secrets and the ConfigMap the chart reads.

| Path | What |
|---|---|
| `backd/` | The chart: workloads, Services, disruption budgets, network policies and the provisioning Job |
| `scripts/registry-prune.sh` | Keeps the newest tags of a registry repository and starts garbage collection |
| `../../scripts/check-chart.sh` | `helm lint`, `helm template` for every `backd/ci/*.yaml`, `kubeconform` and the invariants of `scripts/test-chart.py` |

## The workloads

| Deployment | Image | What it does | Reads the configuration |
|---|---|---|---|
| `backd` | the configuration image | the public API (`serve`; no admin API, no change stream) | yes |
| `backd-realtime` | the configuration image | `serve` for realtime: the only pods that read the change stream and hold the trigger lease | yes |
| `backd-admin` | the configuration image | `serve` with the admin API and interface | yes |
| `worker` | the configuration image | async jobs and cron | yes |
| `executor` | `ghcr.io/fernandezvara/backd-executor` | one Deno process per function call | no |
| `egress` | `ghcr.io/fernandezvara/backd` | the only way out for functions and push | no |
| `backd-provision` (Job) | the configuration image | `backd provision`, as a Helm hook before an install, an upgrade **and a rollback** | yes |

The **configuration image** is `FROM ghcr.io/fernandezvara/backd:<version>` plus the realm tree and the built functions, checked with `backd config check` when it is built. A release is a new tag: `helm upgrade --set image.tag=<release>` is the rollout. Every pod of a release runs exactly the same configuration, and the serving pods start in `PROVISION_MODE=verify`: they refuse a database that differs from their configuration.

A **rollback** is `helm rollback`, which first runs the provisioning Job again with the old image: a pod whose configuration is not the one last provisioned refuses to start, so without it the old pods would crash. A change the old schema rejects (a field it forbids, already stored) needs the data fixed first.

## Installing

The Terraform made the namespace, `backd-mongo`, `backd-keys`, `backd-registry` and `backd-install`. Then:

```sh
helm upgrade --install backd deploy/kubernetes/backd \
  --namespace backd \
  --set image.repository=registry.digitalocean.com/acme/acme \
  --set image.tag=2026-10-30-1 \
  --set networkPolicy.databaseCIDR=10.30.0.0/16 \
  --atomic --wait
```

`image.repository` and `image.tag` are required (the chart says so when they are missing). `networkPolicy.databaseCIDR` is the `DATABASE_CIDR` of the `backd-install` ConfigMap. Use **one namespace per installation**: the Services have fixed names (`backd`, `executor`, `egress`...).

## Replicas

```yaml
replicas:
  default: 2        # every component without its own number
  backd: null       # null = the default
  backdRealtime: 2
  backdAdmin: 1
  worker: 1
  executor: null
  egress: 1
```

A PodDisruptionBudget (`maxUnavailable: 1`) exists for each component with more than one pod. Resource requests and limits are values (`resources.<component>`); the executor's follow `executor.maxProcesses` (each function uses its `memory` plus about 130 MiB): raise them together.

## Security

- Every pod: non-root, `RuntimeDefault` seccomp, read-only root filesystem, no capabilities, no privilege escalation, no service account token. The namespace enforces the `restricted` pod security standard.
- Each workload gets only the secrets it uses: the executor never gets the database, the callback key or `BACKD_SECRETS_KEY`; egress only its key; only the provisioning Job sees the administrative database user.
- Network policies (on by default): nothing is allowed unless a policy says so. The ingress controller reaches the API on 8080; the executor reaches backd's internal listener and egress and nothing else; MongoDB is reached by the serving pods, the worker and the Job only; only egress (and the pods that need HTTPS) reaches the internet, never a private address.
- `trustedProxies` is the private ranges: safe because only the ingress controller's pods can reach backd.

`scripts/check-chart.sh` verifies these properties against the rendered chart.
