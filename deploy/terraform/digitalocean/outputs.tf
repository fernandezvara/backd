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
  value       = data.digitalocean_database_ca.mongo.certificate
}
