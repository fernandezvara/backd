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
  # DigitalOcean's API has no CA for a MongoDB cluster (it answers 404 "CA not found"), so it is
  # given by hand: the cluster has to exist before its CA can be downloaded from the control panel.
  mongo_ca = var.database_ca != null ? var.database_ca : ""

  # The connection string of the cluster ("mongodb+srv://doadmin:...@host/admin?...") is
  # reused for both users: same hosts and options, different credentials.
  mongo_private_uri = digitalocean_database_cluster.mongo.private_uri
  mongo_scheme      = split("://", local.mongo_private_uri)[0]
  mongo_tail        = regex("^[a-z+]+://[^@]+@(.+)$", local.mongo_private_uri)[0]

  # The CA is mounted at this path by the chart (the contract in design/kubernetes.md §7).
  # w=majority only means something with standby nodes.
  # Without the CA the option is left out and the pods fail to verify the database: they are never
  # told to skip the check.
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

# A warning, not an error: the first apply has to run before the CA exists to be downloaded.
check "database_ca" {
  assert {
    condition     = var.database_ca != null
    error_message = "database_ca is not set. The pods cannot verify the database without it: download the CA certificate from the database's page in the control panel (Connection details) and apply again with TF_VAR_database_ca=\"$(cat ca-certificate.crt)\"."
  }
}
