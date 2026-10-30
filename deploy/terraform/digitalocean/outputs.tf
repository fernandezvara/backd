output "cluster_id" {
  description = "The Kubernetes cluster."
  value       = digitalocean_kubernetes_cluster.main.id
}

output "cluster_name" {
  value = digitalocean_kubernetes_cluster.main.name
}

output "kubeconfig" {
  description = "Credentials for kubectl and helm: `terraform output -raw kubeconfig > kubeconfig.yaml`. Anyone who has it controls the cluster."
  value       = digitalocean_kubernetes_cluster.main.kube_config[0].raw_config
  sensitive   = true
}

output "vpc_id" {
  value = digitalocean_vpc.main.id
}

output "vpc_ip_range" {
  value = digitalocean_vpc.main.ip_range
}

output "database_id" {
  description = "The managed MongoDB cluster."
  value       = digitalocean_database_cluster.mongo.id
}

output "mongo_uri" {
  description = "MONGO_URI for the serving pods (the user backd). The CA is expected at /etc/backd/tls/ca.pem."
  value       = local.mongo_uri
  sensitive   = true
}

output "mongo_admin_uri" {
  description = "MONGO_URI of doadmin, for `backd provision` only."
  value       = local.mongo_admin_uri
  sensitive   = true
}

output "mongo_ca" {
  description = "The CA certificate of the managed MongoDB (PEM)."
  value       = local.mongo_ca
}

output "spaces_private" {
  description = "The Space for the private files."
  value = {
    bucket   = digitalocean_spaces_bucket.private.name
    region   = local.spaces_region
    endpoint = local.spaces_url
  }
}

output "spaces_public" {
  description = "The Space for the public files (null when buckets is private): `url` is where browsers read them."
  value = local.has_public ? {
    bucket   = digitalocean_spaces_bucket.public[0].name
    region   = local.spaces_region
    endpoint = local.spaces_url
    url      = local.public_url
  } : null
}

output "spaces_private_key" {
  description = "The key of the private Space: the realm secrets STORAGE_ACCESS_KEY and STORAGE_SECRET_KEY (`backd secret set`)."
  value = {
    access_key = digitalocean_spaces_key.private.access_key
    secret_key = digitalocean_spaces_key.private.secret_key
  }
  sensitive = true
}

output "spaces_public_key" {
  description = "The key of the public Space: the realm secrets PUBLIC_STORAGE_ACCESS_KEY and PUBLIC_STORAGE_SECRET_KEY."
  value = local.has_public ? {
    access_key = digitalocean_spaces_key.public[0].access_key
    secret_key = digitalocean_spaces_key.public[0].secret_key
  } : null
  sensitive = true
}

output "realm_storage" {
  description = "The `storage:` (and `public_storage:`) section for the realm's realm.yaml: `terraform output -raw realm_storage`. Pick a different prefix per installation sharing a Space."
  value = <<-EOT
    storage:
      provider: digitalocean
      region: ${local.spaces_region}
      bucket: ${digitalocean_spaces_bucket.private.name}
      prefix: ${var.install}
      access_key: secret:STORAGE_ACCESS_KEY
      secret_key: secret:STORAGE_SECRET_KEY
    ${local.has_public ? <<-PUB
    public_storage:
      provider: digitalocean
      region: ${local.spaces_region}
      bucket: ${var.public_bucket_name}
      prefix: ${var.install}
      access_key: secret:PUBLIC_STORAGE_ACCESS_KEY
      secret_key: secret:PUBLIC_STORAGE_SECRET_KEY
      url: ${local.public_url}
    PUB
: ""}
  EOT
}

output "registry_name" {
  description = "The registry."
  value       = digitalocean_container_registry.main.name
}

output "registry_endpoint" {
  description = "Where images are pushed and pulled: registry.digitalocean.com/<name>. The configuration image of an installation is <endpoint>/<install>:<release>."
  value       = digitalocean_container_registry.main.endpoint
}

output "registry_push_credentials" {
  description = "The Docker config (JSON) the pipeline logs in with: it can push. Store it as a CI secret."
  value       = digitalocean_container_registry_docker_credentials.push.docker_credentials
  sensitive   = true
}
