data "digitalocean_kubernetes_versions" "this" {
  version_prefix = var.kubernetes_version_prefix
}

locals {
  autoscale = var.node_min != null
}

resource "digitalocean_kubernetes_cluster" "main" {
  name     = var.install
  region   = var.region
  version  = data.digitalocean_kubernetes_versions.this.latest_version
  vpc_uuid = digitalocean_vpc.main.id
  tags     = local.tags

  ha            = var.ha_control_plane
  auto_upgrade  = var.kubernetes_auto_upgrade
  surge_upgrade = true

  node_pool {
    name       = "${var.install}-workers"
    size       = var.node_size
    node_count = local.autoscale ? null : var.node_count
    auto_scale = local.autoscale
    min_nodes  = var.node_min
    max_nodes  = var.node_max
    tags       = [local.node_tag]
  }

  maintenance_policy {
    day        = var.maintenance_day
    start_time = var.maintenance_start
  }

  dynamic "control_plane_firewall" {
    for_each = length(var.control_plane_allowed_cidrs) > 0 ? [1] : []
    content {
      enabled           = true
      allowed_addresses = var.control_plane_allowed_cidrs
    }
  }

  lifecycle {
    # A version bump is done by the maintenance window or on purpose; a newer "latest" must not
    # plan an upgrade of a running cluster.
    ignore_changes = [version]
  }
}
