# The registry the pipeline pushes the configuration image to (design/kubernetes.md §4). One
# repository is enough for an installation, so every tier works; the cluster pulls through the
# integration (cluster.tf), with no credential to manage.

locals {
  registry_name = coalesce(var.registry_name, var.install)
}

resource "digitalocean_container_registry" "main" {
  name                   = local.registry_name
  subscription_tier_slug = var.registry_tier
  region                 = coalesce(var.registry_region, var.region)
}

# What the pipeline logs in with: it can push. Anything the cluster pulls with is read-only.
resource "digitalocean_container_registry_docker_credentials" "push" {
  registry_name  = digitalocean_container_registry.main.name
  write          = true
  expiry_seconds = var.registry_push_credential_days > 0 ? var.registry_push_credential_days * 86400 : null
}

# A pull secret for namespaces the integration doesn't cover (it covers the cluster's default
# ones): used by the Secrets of the next steps.
resource "digitalocean_container_registry_docker_credentials" "pull" {
  registry_name = digitalocean_container_registry.main.name
  write         = false
}
