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
