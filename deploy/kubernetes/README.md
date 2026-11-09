# backd on Kubernetes

The Helm chart of backd (`backd/`), the scripts around it, and the design behind them: [`design/kubernetes.md`](../../design/kubernetes.md). The infrastructure it runs on is made by Terraform ([`deploy/terraform/digitalocean`](../terraform/digitalocean/README.md) today; `deploy/terraform/<provider>` for others), which also creates the Secrets and the ConfigMap the chart reads.

| Path | What |
|---|---|
| `backd/` | The chart: workloads, Services, disruption budgets, network policies and the provisioning Job |
| `scripts/registry-prune.sh` | Keeps the newest tags of a registry repository and starts garbage collection |
| `scripts/admin-allow.sh` | Changes who may reach the admin host, in place |
| `pipeline/` | The configuration image's `Dockerfile`, an example GitHub Actions workflow (`release.yml`), `values.example.yaml` and the guard against realms without authentication |
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

## Traffic in

Traefik (installed by the Terraform; ingress-nginx was retired in March 2026) ends TLS with certificates cert-manager issues for the two hosts, and the chart declares what it does with each request (`ingress.enabled`, on by default; `ingress.hosts.api` and `ingress.hosts.admin` are the `API_HOST` and `ADMIN_HOST` of the install ConfigMap):

- **The public host** routes `/v1/` only (`/_ui/`, `/healthz`, `/readyz` and `/metrics` are not reachable from outside). Limits are per client address and are the numbers of the Compose reference's nginx, in `ingress.limits`: 20 requests a second with a burst of 40 for everything, 10 a minute with a burst of 5 for sign-up, login and the account flows (before the general one), 2 a second with a burst of 5 for calling a function, 20 simultaneous requests, and a 1 MiB body (not for files, whose limits are backd's). HTTP is redirected to HTTPS and every response carries HSTS.
- **Realtime** (`POST /v1/{realm}/_realtime`) goes to the `backd-realtime` pods, the only ones reading the change stream, and has no limit on how long it lasts.
- **The admin host** goes to `backd-admin` (the admin API and the interface at `/_ui/`) **from the allowed addresses only**, checked before anything else.

### Who may reach the admin host

`ingress.admin.allowedCIDRs` is required on the first install (`--set 'ingress.admin.allowedCIDRs={203.0.113.7/32}'`): the chart refuses to render with an empty list, so the host is never opened to everyone. To change it later without a release:

```sh
deploy/kubernetes/scripts/admin-allow.sh 203.0.113.7/32,198.51.100.0/24
deploy/kubernetes/scripts/admin-allow.sh --show
```

The script patches the Middleware and keeps the list in the ConfigMap `backd-admin-allow`, which the chart reads on every `helm upgrade` and which **wins over the values** (the values only seed the first install), so a release does not undo your change. It refuses `0.0.0.0/0`, `::/0` and any `/0`, and an address without a prefix.

If your address changed and the admin host refuses you, you are not locked out of the cluster: the list concerns HTTP to that host only, and `kubectl` and Terraform use the Kubernetes and DigitalOcean APIs. Run the script with your cluster credentials, or reach the admin instance directly with `kubectl port-forward svc/backd-admin 8081:8080 -n backd`.

## The release pipeline

The configuration lives in its own repository (`config/` is `CONFIG_DIR`, as in [Deploying config](../../docs/content/docs/operate/deploying.md)). `pipeline/release.yml` is an example workflow for it: on a tag `release-<anything>` it

1. refuses a realm with `auth: disabled` (`pipeline/check-no-auth-disabled.sh`: such a realm has no authentication and this installation has a public address);
2. builds the configuration image from `pipeline/Dockerfile`: the functions are bundled in a stage on the released executor image (which has the Deno this backd release runs functions with) and `backd config check` runs there and again in the final image, so **an invalid configuration fails the build and nothing is pushed**; the final image is the released `backd` image plus `/config`, with no Deno and no shell;
3. pushes it to the registry as `<registry>/<name>:<tag>` (the git tag; one repository is enough, so it works on the Starter tier);
4. runs `helm upgrade --install --atomic --wait` with `image.tag=<tag>`: the provisioning hook runs first with the new image, the rollout is the restart, and a rollout that does not become ready rolls back;
5. prunes old tags (`scripts/registry-prune.sh`, never the running one).

The chart, the Dockerfile and the scripts are checked out from the backd release in `BACKD_VERSION`, so the configuration repository holds only `config/`, `deploy/values.yaml` (start from `pipeline/values.example.yaml`) and the workflow. The variables and the secret it needs are listed at the top of `release.yml`; the job runs in a protected `production` environment, so a person can approve each release.

**Rollback:** run the workflow on the previous tag, or `helm rollback backd <revision> -n backd --wait` (the provisioning Job runs first, with the old image). The registry keeps the last ten tags.

**The `auth: disabled` guard** is a step, not a setting: if you are certain a realm without authentication belongs in this installation (it does not belong on the internet), remove the step on purpose and say why in the commit. Nothing else in the pipeline checks it.
