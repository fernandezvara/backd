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

# ---------------------------------------------------------------- certificates

resource "helm_release" "cert_manager" {
  name             = "cert-manager"
  repository       = "https://charts.jetstack.io"
  chart            = "cert-manager"
  version          = var.cert_manager_chart_version
  namespace        = "cert-manager"
  create_namespace = true
  wait             = true
  timeout          = 600

  values = [yamlencode({
    crds       = { enabled = true }
    prometheus = { enabled = false }
  })]
}

# The token cert-manager answers DNS-01 challenges with.
resource "kubernetes_secret" "dns_token" {
  metadata {
    name      = "digitalocean-dns"
    namespace = "cert-manager"
  }

  data = {
    "access-token" = var.dns_token
  }

  depends_on = [helm_release.cert_manager]
}

# The ClusterIssuer is a custom resource of cert-manager's, so it is installed by a chart of its
# own after cert-manager is ready (a manifest would need the cluster at plan time).
resource "helm_release" "cluster_issuer" {
  name      = "cluster-issuer"
  chart     = "${path.module}/charts/cluster-issuer"
  namespace = "cert-manager"
  wait      = true

  values = [yamlencode({
    name   = local.cluster_issuer
    email  = var.acme_email
    server = var.acme_staging ? "https://acme-staging-v02.api.letsencrypt.org/directory" : "https://acme-v02.api.letsencrypt.org/directory"
    tokenSecret = {
      name = kubernetes_secret.dns_token.metadata[0].name
      key  = "access-token"
    }
  })]
}

locals {
  cluster_issuer = "letsencrypt"
}
