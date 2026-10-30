# DigitalOcean's managed MongoDB, on the private network, reachable from the cluster only.
#
# What DigitalOcean does not allow (documented in design/kubernetes.md and the security pages):
# no custom roles and no ACLs, and every user gets the permissions of `doadmin`. The user
# backd serves with is therefore as strong as the one provisioning uses; what keeps the
# database closed is the network (no public path, one trusted source) and, in the cluster,
# network policies that let only backd's pods connect.

resource "digitalocean_database_cluster" "mongo" {
  name       = "${var.install}-mongo"
  engine     = "mongodb"
  version    = var.database_version
  region     = var.region
  size       = var.database_size
  node_count = var.database_nodes

  storage_size_mib     = var.database_storage_mib
  private_network_uuid = digitalocean_vpc.main.id
  tags                 = local.tags

  maintenance_window {
    day  = var.database_maintenance_day
    hour = var.database_maintenance_hour
  }
}

resource "digitalocean_database_firewall" "mongo" {
  cluster_id = digitalocean_database_cluster.mongo.id

  rule {
    type  = "k8s"
    value = digitalocean_kubernetes_cluster.main.id
  }

  dynamic "rule" {
    for_each = toset(var.database_allowed_cidrs)
    content {
      type  = "ip_addr"
      value = rule.value
    }
  }
}

# The user the serving pods connect as. `doadmin` stays for provisioning only.
resource "digitalocean_database_user" "app" {
  cluster_id = digitalocean_database_cluster.mongo.id
  name       = "backd"

  lifecycle {
    # The name is all there is to set. The provider would otherwise plan an update of the user's
    # settings, which DigitalOcean answers with 400 "missing the following required fields:
    # user_settings" for a MongoDB user.
    ignore_changes = all
  }
}

locals {
  # DigitalOcean's MongoDB presents a Let's Encrypt certificate, which the pods verify against the
  # system roots, so no CA is needed (the API has none for MongoDB anyway: 404 "CA not found").
  # database_ca is an optional override for a CA of your own.
  mongo_ca = var.database_ca != null ? var.database_ca : ""

  # The connection string of the cluster ("mongodb+srv://doadmin:...@host/admin?...") is
  # reused for both users: same hosts and options, different credentials.
  mongo_private_uri = digitalocean_database_cluster.mongo.private_uri
  mongo_scheme      = split("://", local.mongo_private_uri)[0]
  # The host comes from private_host, not from the URI: for MongoDB the provider's private_uri
  # carried the public name, which resolves to addresses the pods cannot reach (the database only
  # accepts the cluster over the VPC). Only the database and the options are taken from the URI.
  mongo_tail = "${digitalocean_database_cluster.mongo.private_host}${regex("^[a-z+]+://[^@]+@[^/?]+(.*)$", local.mongo_private_uri)[0]}"

  # The CA is mounted at this path by the chart (the contract in design/kubernetes.md §7).
  # w=majority only means something with standby nodes.
  # Without a CA the system roots verify the database: the check is never skipped.
  mongo_options = join("", [
    strcontains(local.mongo_tail, "?") ? "&" : "?",
    local.mongo_ca != "" ? "tlsCAFile=/etc/backd/tls/ca.pem" : "tls=true",
    var.database_nodes == 3 ? "&w=majority" : "",
  ])

  # urlencode writes a space as "+", which a connection string reads as a plus.
  mongo_app_password   = replace(urlencode(digitalocean_database_user.app.password), "+", "%20")
  mongo_admin_password = replace(urlencode(digitalocean_database_cluster.mongo.password), "+", "%20")

  mongo_uri       = "${local.mongo_scheme}://${digitalocean_database_user.app.name}:${local.mongo_app_password}@${local.mongo_tail}${local.mongo_options}"
  mongo_admin_uri = "${local.mongo_scheme}://${digitalocean_database_cluster.mongo.user}:${local.mongo_admin_password}@${local.mongo_tail}${local.mongo_options}"
}
