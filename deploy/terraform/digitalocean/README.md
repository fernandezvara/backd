# Terraform: backd on DigitalOcean

Builds what a backd installation needs on DigitalOcean (design: [`design/kubernetes.md`](../../../design/kubernetes.md)). The pieces are added one issue at a time; this file lists what exists.

| Directory | What it builds |
|---|---|
| `bootstrap/` | The private, versioned Space that holds the state of everything else, and a key bound to it |
| `.` | The installation itself (grows with each step) |

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
