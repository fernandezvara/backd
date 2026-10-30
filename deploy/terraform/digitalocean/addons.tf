# The ingress controller. Its Service asks DigitalOcean for the installation's one load balancer
# (the annotations in values/traefik.yaml.tftpl), so the load balancer lives and dies with it and
# Terraform reads it back (loadbalancer.tf) for DNS and the node firewall.

resource "helm_release" "traefik" {
  name             = "traefik"
  repository       = "https://traefik.github.io/charts"
  chart            = "traefik"
  version          = var.traefik_chart_version
  namespace        = "traefik"
  create_namespace = true

  # Waits for the load balancer to get its address.
  wait    = true
  timeout = 600

  values = [templatefile("${path.module}/values/traefik.yaml.tftpl", {
    replicas     = var.ingress_replicas
    trusted_ips  = jsonencode([digitalocean_vpc.main.ip_range])
    lb_name      = local.lb_name
    lb_size      = var.lb_size
    lb_algorithm = var.lb_algorithm
    lb_hostname  = var.api_host
  })]
}
