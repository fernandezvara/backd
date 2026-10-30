locals {
  lb_name = "${var.install}-ingress"
}

# Made by the ingress controller's Service (addons.tf); read back by name.
data "digitalocean_loadbalancer" "ingress" {
  name       = local.lb_name
  depends_on = [helm_release.traefik]
}
