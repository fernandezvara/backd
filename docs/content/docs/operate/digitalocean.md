---
title: "Deploying on DigitalOcean"
description: "From an empty account to a running installation: Terraform builds the cluster, the managed MongoDB, the Spaces, the registry, the load balancer and the DNS records; a pipeline deploys the configuration."
icon: "cloud"
weight: 312
toc: true
---

This is the path from an empty DigitalOcean account to a running `backd`: **Terraform** builds the infrastructure (a Kubernetes cluster, a managed MongoDB, one or two Spaces, a container registry, a load balancer and the DNS records) and creates the secrets, and a **pipeline** in your configuration repository builds the image of your configuration and deploys it with the [Helm chart](../kubernetes/). The files are in `deploy/terraform/digitalocean` and `deploy/kubernetes` of the repository; the reasoning is in `design/kubernetes.md`.

{{< hint style="warning" >}}
A realm with `auth: disabled` has no authentication and this installation has a public address. The example pipeline refuses to build such a configuration; do not remove that step to make a demo work. See the [security model](../administration/security/).
{{< /hint >}}

## What you get, and what it costs

| Part | Default | $ a month |
|---|---|---|
| Kubernetes cluster | standard (free) control plane, 2 nodes of 2 vCPU / 4 GB | 48 |
| Load balancer | one `lb-small`, for the API and the admin host | 12 |
| MongoDB | managed, 1 node of 1 vCPU / 2 GB | 30.51 |
| Spaces | one private Space (250 GiB and 1 TiB of transfer included) | 5 |
| Container registry | `basic` (`starter` is free and works: one repository) | 5 |
| **Total** | | **about 100** |

The choices that change the price: `database_nodes = 3` (a primary and two standby nodes, three times the database: about $91.53 for the 2 GB size), a bigger `database_size`, `node_size` and `node_count`, a high-availability control plane (`ha_control_plane`, about $40), a second Space for public files, and `registry_tier`. Prices are DigitalOcean's at the time of writing; check their pricing pages.

## What you need

- A DigitalOcean account, an API token with read and write access, and one Spaces key that can create buckets (Spaces keys that Terraform makes are bound to one Space, so this first one is yours: API > Spaces Keys).
- A domain on DigitalOcean DNS (Networking > Domains), with its name servers pointing there.
- A second API token for cert-manager with write access to **Domains only**.
- Terraform 1.9 or later, `kubectl` and `helm`.
- A repository for your configuration (`config/` is `CONFIG_DIR`, as in [Deploying config](../deploying/)).

## 1. The state bucket

The Terraform state holds every generated secret, so it lives in a private Space of its own, made once by `bootstrap/`:

```sh
export DIGITALOCEAN_TOKEN=...  SPACES_ACCESS_KEY_ID=...  SPACES_SECRET_ACCESS_KEY=...
cd deploy/terraform/digitalocean/bootstrap
terraform init
terraform apply -var state_bucket_name=acme-backd-state -var region=fra1
terraform output -raw backend_config > ../backend.hcl
export AWS_ACCESS_KEY_ID=$(terraform output -raw access_key_id)
export AWS_SECRET_ACCESS_KEY=$(terraform output -raw secret_access_key)
```

## 2. The installation

```sh
cd ..                                   # deploy/terraform/digitalocean
cp terraform.tfvars.example terraform.tfvars   # fill it in: the file explains each variable
export TF_VAR_dns_token=...             # the Domains-only token
terraform init -backend-config=backend.hcl
terraform apply
```

The first apply ends with a warning that `database_ca` is not set. DigitalOcean's API has no CA certificate for a MongoDB cluster, so you give it: open the database in the control panel, download the CA certificate (Connection details), and apply again:

```sh
export TF_VAR_database_ca="$(cat ca-certificate.crt)"
terraform apply            # only updates the Secret the pods read
```

Without it the connection string leaves out the CA and the pods cannot verify the database (they are never told to skip the check).

Set `acme_staging = true` for the first run (certificates browsers do not trust, with far higher limits) and switch it off once everything works. The apply takes ten to fifteen minutes, most of it the cluster and the database.

| Variable | What it changes |
|---|---|
| `install` | The name of everything (prefixes, tags, `BACKD_DEPLOYMENT_ID`) |
| `node_size`, `node_count`, `node_min` + `node_max` | The workers, or autoscaling between two numbers |
| `database_nodes` | `1`, or `3` for a primary and two standby nodes; nothing in between |
| `database_size`, `database_version` | The size of each database node, and the MongoDB version |
| `database_ca` | The database's CA certificate, downloaded from the control panel after the first apply |
| `buckets` | `private`, or `private_and_public` (the second Space holds the files of `public: true` fields) |
| `private_bucket_name`, `public_bucket_name` | The names of the Spaces (unique per region) |
| `public_cdn`, `assets_host` | A CDN in front of the public Space, optionally under your own name |
| `registry_tier` | `starter`, `basic` (default) or `professional` |
| `domain`, `api_host`, `admin_host` | Where the installation answers |
| `lb_size`, `lb_algorithm` | The load balancer |
| `control_plane_allowed_cidrs`, `database_allowed_cidrs` | Extra restrictions on the Kubernetes API and the database |

Nothing on the nodes is reachable from the internet: the firewall allows only the private network, the database accepts only the cluster, and the Spaces and the registry are private.

## 3. The first release

Put the pipeline in your configuration repository ([Kubernetes](../kubernetes/#the-release-pipeline)): `deploy/kubernetes/pipeline/release.yml` as `.github/workflows/release.yml`, and `deploy/values.yaml` from `values.example.yaml` with the values Terraform printed. Set the repository variables and the secret the workflow lists (`terraform output registry_endpoint`, `cluster_name`, and a token for the pipeline), then tag:

```sh
git tag release-2026-10-30-1 && git push origin release-2026-10-30-1
```

The workflow checks the configuration, builds and pushes the image, upgrades the chart, and the pods start in verify mode against a database it has just provisioned.

## 4. The first administrator and the storage keys

Create the first administrator of each realm from inside the cluster, then give the realm its storage keys (they are *realm secrets*, kept encrypted in MongoDB, not environment variables):

```sh
kubectl exec -n backd deploy/backd-admin -- /backd bootstrap --realm shop --email me@example.com

terraform output -json spaces_private_key      # access_key, secret_key
backd secret set --realm shop --name STORAGE_ACCESS_KEY --url https://admin.example.com
backd secret set --realm shop --name STORAGE_SECRET_KEY --url https://admin.example.com
backd storage check --realm shop --url https://admin.example.com
```

`terraform output -raw realm_storage` prints the `storage:` (and `public_storage:`) section for the realm's `realm.yaml`. The admin host answers only from the addresses you gave in `ingress.admin.allowedCIDRs`; see [Kubernetes](../kubernetes/#who-may-reach-the-admin-host) for changing them.

## Changing the installation

- **From one database node to three:** set `database_nodes = 3` and apply. DigitalOcean adds the standby nodes to the same cluster, the connection string gains `w=majority`, and the new URI reaches the pods with the next rollout. The configuration and the data do not change.
- **A bigger cluster:** change `node_size` or `node_count` and apply.
- **A new configuration:** tag the configuration repository. A rollback is the previous tag, or `helm rollback`.
- **A new backd release:** change `BACKD_VERSION` in the workflow's variables and tag; read the *Upgrading* section of [Operate](../#upgrading-to-v096) first.
- **Destroying it:** `terraform destroy` removes the infrastructure. The Spaces are never emptied by Terraform: if they hold files the destroy stops until you empty them yourself, and the state bucket from step 1 is yours to remove.

## What DigitalOcean does not allow

{{< hint style="warning" title="A weaker database posture than the Compose reference" >}}
DigitalOcean's managed MongoDB has no custom roles and no access control lists, and every user it creates gets the permissions of `doadmin`. The [production reference](../production/#least-privilege-provisioning) gives `backd` a user that can read and write documents and nothing else; here the user `backd` serves with can do whatever the provisioning user can, and the audit and invocation history collections are not append-only. `PROVISION_MODE=verify` still stops a pod from changing the schema on its own, but the database would let it. What keeps the database closed is the network: no public path, the cluster as its only trusted source, and network policies that let only the `backd` pods connect to it.
{{< /hint >}}

The other limits worth knowing: a Spaces key reaches every prefix of its Space (use a Space per installation), and a one-node database has no failover and goes down for its maintenance window.
