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
}

# The CA, from the variable when it is given, else from the API (which answered "CA not found"
# right after a cluster was created).
data "digitalocean_database_ca" "mongo" {
  count      = var.database_ca == null ? 1 : 0
  cluster_id = digitalocean_database_cluster.mongo.id
}

locals {
  mongo_ca = var.database_ca != null ? var.database_ca : data.digitalocean_database_ca.mongo[0].certificate

  # The connection string of the cluster ("mongodb+srv://doadmin:...@host/admin?...") is
  # reused for both users: same hosts and options, different credentials.
  mongo_private_uri = digitalocean_database_cluster.mongo.private_uri
  mongo_scheme      = split("://", local.mongo_private_uri)[0]
  mongo_tail        = regex("^[a-z+]+://[^@]+@(.+)$", local.mongo_private_uri)[0]

  # The CA is mounted at this path by the chart (the contract in design/kubernetes.md §7).
  # w=majority only means something with standby nodes.
  mongo_options = join("", [
    strcontains(local.mongo_tail, "?") ? "&" : "?",
    "tlsCAFile=/etc/backd/tls/ca.pem",
    var.database_nodes == 3 ? "&w=majority" : "",
  ])

  # urlencode writes a space as "+", which a connection string reads as a plus.
  mongo_app_password   = replace(urlencode(digitalocean_database_user.app.password), "+", "%20")
  mongo_admin_password = replace(urlencode(digitalocean_database_cluster.mongo.password), "+", "%20")

  mongo_uri       = "${local.mongo_scheme}://${digitalocean_database_user.app.name}:${local.mongo_app_password}@${local.mongo_tail}${local.mongo_options}"
  mongo_admin_uri = "${local.mongo_scheme}://${digitalocean_database_cluster.mongo.user}:${local.mongo_admin_password}@${local.mongo_tail}${local.mongo_options}"
}
