# Terraform: backd on DigitalOcean

Builds what a backd installation needs on DigitalOcean (design: [`design/kubernetes.md`](../../../design/kubernetes.md)). The pieces are added one issue at a time; this file lists what exists.

| Directory | What it builds |
|---|---|
| `bootstrap/` | The private, versioned Space that holds the state of everything else, and a key bound to it |
| `.` | The installation itself (grows with each step): `network.tf` (private network, node firewall), `cluster.tf` (the DOKS cluster), `database.tf` (the managed MongoDB) |

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

- The nodes have no open port to the internet: the firewall `<install>-nodes` allows inbound traffic from the private network only (the load balancer joins it in a later step). DigitalOcean keeps a firewall of its own on every cluster for what Kubernetes needs; the rules of both add up.
- `control_plane_allowed_cidrs` restricts the Kubernetes API to the addresses you list. Empty leaves it reachable with the cluster's credentials only; set it once you know your addresses (and your pipeline's).
- A newer Kubernetes version never plans an upgrade of a running cluster (`ignore_changes = [version]`): upgrades come from the maintenance window (`kubernetes_auto_upgrade`) or from you.
- `terraform output -raw kubeconfig > kubeconfig.yaml` gives the credentials for `kubectl` and `helm`.

## The database

`database_nodes` is `1` (the default: no failover, and a maintenance window is downtime) or `3` (a primary and two standby nodes; DigitalOcean offers nothing in between, and the plan fails with a message for any other number). `database_size` is a managed database slug (`db-s-1vcpu-2gb` by default; three nodes cost three times one) and `database_version` the MongoDB major version. The database is on the private network and its only trusted source is the cluster; `database_allowed_cidrs` adds addresses (your own, to restore a backup).

**Going from one node to three** is a change of `database_nodes` in the variables and `terraform apply`: DigitalOcean adds the two standby nodes to the same cluster, the connection string gains `w=majority`, and the new `MONGO_URI` reaches the pods with the next rollout. Nothing in the configuration or the data changes.

**What DigitalOcean does not allow.** There are no custom roles and no ACLs, and every user gets the permissions of `doadmin`, so the user backd serves with (`backd`) can do what the provisioning user can. The production reference gives backd's user document access only; this installation cannot. What keeps the database closed is the network: no public path, the cluster as the only trusted source, and the network policies of the Helm chart that let only backd's pods connect to it. `PROVISION_MODE=verify` still stops a pod from changing the schema on its own, but the database would let it.

`mongo_uri` and `mongo_admin_uri` are sensitive outputs (they go into the cluster's Secrets in a later step) and `mongo_ca` is the certificate the pods trust (mounted at `/etc/backd/tls/ca.pem`, which the URIs name).
