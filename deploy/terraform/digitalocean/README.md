# Terraform: backd on DigitalOcean

Builds what a backd installation needs on DigitalOcean (design: [`design/kubernetes.md`](../../../design/kubernetes.md)). The pieces are added one issue at a time; this file lists what exists.

| Directory | What it builds |
|---|---|
| `bootstrap/` | The private, versioned Space that holds the state of everything else, and a key bound to it |
| `.` | The installation itself (grows with each step): `network.tf` (private network, node firewall), `cluster.tf` (the DOKS cluster), `database.tf` (the managed MongoDB), `spaces.tf` (the Spaces), `registry.tf` (the container registry), `addons.tf` (Traefik), `loadbalancer.tf`, `dns.tf` |

## First step: the state bucket

The state holds every generated secret, so it must not live on a laptop. `bootstrap/` makes a private Space for it. You need an API token and one Spaces key that can create buckets (Spaces keys that Terraform makes are bound to one bucket, so this first one is yours: create it in the control panel under API > Spaces Keys).

```sh
export DIGITALOCEAN_TOKEN=...
export SPACES_ACCESS_KEY_ID=...
export SPACES_SECRET_ACCESS_KEY=...

cd bootstrap
terraform init
terraform apply -var state_bucket_name=acme-backd-state -var region=fra1
terraform output -raw backend_config > ../backend.hcl
export AWS_ACCESS_KEY_ID=$(terraform output -raw access_key_id)
export AWS_SECRET_ACCESS_KEY=$(terraform output -raw secret_access_key)

cd ..
terraform init -backend-config=backend.hcl
```

The bootstrap's own state is local (`bootstrap/terraform.tfstate`, gitignored) and holds the state key's secret: keep it somewhere safe, or print the two outputs once, store them in a password manager and delete the file (the bucket stays; the key can be replaced in the panel). Terraform never deletes the state bucket (`force_destroy = false`).

## Checking the files without an account

`scripts/check-terraform.sh` runs `terraform fmt -check`, `init -backend=false`, `validate` and `terraform test` on every configuration. The tests mock the DigitalOcean provider, so they plan offline against nothing and need no token.

## The cluster

`install` names the installation (every resource and tag carries it). `node_size`, `node_count` (or `node_min` and `node_max` for autoscaling), `ha_control_plane`, `kubernetes_version_prefix` and the maintenance window are variables; the defaults are two nodes of 2 vCPU / 4 GB and a standard (free) control plane.

- The nodes have no open port to the internet: the firewall `<install>-nodes` allows inbound traffic from the private network only (the load balancer is in it, so it reaches the nodes from inside). DigitalOcean keeps a firewall of its own on every cluster for what Kubernetes needs; the rules of both add up.
- `control_plane_allowed_cidrs` restricts the Kubernetes API to the addresses you list. Empty leaves it reachable with the cluster's credentials only; set it once you know your addresses (and your pipeline's).
- A newer Kubernetes version never plans an upgrade of a running cluster (`ignore_changes = [version]`): upgrades come from the maintenance window (`kubernetes_auto_upgrade`) or from you.
- `terraform output -raw kubeconfig > kubeconfig.yaml` gives the credentials for `kubectl` and `helm`.

## The database

`database_nodes` is `1` (the default: no failover, and a maintenance window is downtime) or `3` (a primary and two standby nodes; DigitalOcean offers nothing in between, and the plan fails with a message for any other number). `database_size` is a managed database slug (`db-s-1vcpu-2gb` by default; three nodes cost three times one) and `database_version` the MongoDB major version. The database is on the private network and its only trusted source is the cluster; `database_allowed_cidrs` adds addresses (your own, to restore a backup).

**Going from one node to three** is a change of `database_nodes` in the variables and `terraform apply`: DigitalOcean adds the two standby nodes to the same cluster, the connection string gains `w=majority`, and the new `MONGO_URI` reaches the pods with the next rollout. Nothing in the configuration or the data changes.

**What DigitalOcean does not allow.** There are no custom roles and no ACLs, and every user gets the permissions of `doadmin`, so the user backd serves with (`backd`) can do what the provisioning user can. The production reference gives backd's user document access only; this installation cannot. What keeps the database closed is the network: no public path, the cluster as the only trusted source, and the network policies of the Helm chart that let only backd's pods connect to it. `PROVISION_MODE=verify` still stops a pod from changing the schema on its own, but the database would let it.

`mongo_uri` and `mongo_admin_uri` are sensitive outputs (they go into the cluster's Secrets in a later step) and `mongo_ca` is the certificate the pods trust (mounted at `/etc/backd/tls/ca.pem`, which the URIs name).

## The Spaces

`buckets = "private"` makes one Space (`private_bucket_name`) for the private files; `"private_and_public"` adds a second (`public_bucket_name`) for the files of fields with `public: true`. Names are yours and unique per region; the plan fails with a message when the public one is missing or equals the private one.

- **Private:** never public, no CDN. `private_cors_origins` adds the CORS rule browsers need for direct uploads and signed downloads (`PUT` and the `Content-Type` and `x-amz-checksum-sha256` headers); empty means uploads go through backd only.
- **Public:** the Space stays `private` to the ACL (`public-read` would let anyone list it) and a bucket policy allows `s3:GetObject` to anyone, so a file can be read at its address and the Space cannot be listed. `public_cdn` puts DigitalOcean's CDN in front; with `assets_host` (a name in a domain on DigitalOcean DNS) the files are served from that name over HTTPS with a Let's Encrypt certificate, else from the CDN's own address. `spaces_public.url` is the address for `public_storage.url`.
- **Keys:** one per Space, bound to it, so the private key cannot touch the public Space and the other way round (`spaces_key_permission`: `readwrite` by default, or `read`; DigitalOcean refuses `fullaccess` on a key bound to one Space). They are sensitive outputs (`spaces_private_key`, `spaces_public_key`) and become **realm secrets** (`backd secret set STORAGE_ACCESS_KEY`, ...), not pod environment: backd stores them encrypted in MongoDB.
- `terraform output -raw realm_storage` prints the `storage:` and `public_storage:` sections for the realm's `realm.yaml`. A key reaches every prefix of its Space, so use a Space per installation, or accept it.
- Spaces are never emptied by Terraform (`force_destroy = false`): destroying an installation with files in them fails until you empty them yourself. Unfinished multipart uploads are removed after `abort_incomplete_uploads_days`.

## The registry

`registry_tier` is `starter` (one repository, 500 MiB, free), `basic` (the default: five repositories, 5 GiB) or `professional` (unlimited, 100 GiB). One repository is enough for an installation on every tier: the pipeline pushes the configuration image as `<registry endpoint>/<install>:<release>`, and the executor and egress images come from `ghcr.io`. `registry_name` (unique across DigitalOcean, the installation's name by default) and `registry_region` are variables.

- **Pulling:** the cluster is integrated with the registry (`registry_integration`), so pods pull with no credential to manage.
- **Pushing:** `terraform output -raw registry_push_credentials` is a Docker config that can push; store it as a CI secret. It does not expire unless `registry_push_credential_days` says so; replace it with `terraform apply -replace=digitalocean_container_registry_docker_credentials.push`.
- **Old releases fill the registry** (about ten releases of a 50 MiB image fill Starter). `deploy/kubernetes/scripts/registry-prune.sh <repository> [keep]` keeps the newest tags (10 by default), deletes the rest and starts the registry's garbage collection, which is what actually frees the space (`DRY_RUN=1` lists what it would delete; `KEEP_TAGS="r12 r11"` protects tags, for example the one that is running). The pipeline of a later step runs it after a successful rollout.

## Traffic: the load balancer and DNS

The installation has **one load balancer**. It is requested by the Service of the ingress controller (**Traefik**; ingress-nginx was retired in March 2026), so its size and algorithm are the variables `lb_size` and `lb_algorithm`, and Terraform reads it back by name (`<install>-ingress`) for the DNS records and the nodes' firewall. It forwards TCP with the PROXY protocol, so Traefik ends TLS and still sees the client's address; Traefik trusts that header only from the private network. The Service has `externalTrafficPolicy: Local`: a node without a Traefik pod fails the load balancer's health check and gets no traffic (`ingress_replicas` pods, spread over nodes).

- `domain` must already exist on DigitalOcean DNS (Networking > Domains) and its name servers must point there; the plan fails with "not found" when it does not.
- `api_host` and `admin_host` (names inside the domain, different from each other) get an A record each, pointing at the load balancer (`dns_ttl`). With `public_cdn` and `assets_host`, the assets name is a CNAME of the CDN.
- The records for TLS challenges are made by cert-manager (a later step).
- Destroying the installation removes the load balancer with Traefik's release.
- **Cost:** `lb-small` is about $12 a month; the add-on adds nothing else.

**The CA.** The pods verify the database's TLS certificate against its CA. Terraform asks DigitalOcean's API for it; if the API answers "CA not found" (it did right after a cluster was created, and may for MongoDB), download the certificate from the database page of the control panel (Connection details > CA certificate) and pass its text as `database_ca`: `export TF_VAR_database_ca="$(cat ca-certificate.crt)"`.
