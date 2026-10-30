# The namespace and the objects the Helm chart reads (design/kubernetes.md §7). The chart
# creates none of them; Terraform creates all of them, so the chart can be installed by a
# pipeline that holds no secret except the cluster's credentials.
#
# The Spaces keys are not here: backd keeps them as realm secrets in MongoDB (`backd secret set`).

resource "kubernetes_namespace" "backd" {
  metadata {
    name = var.namespace
    labels = {
      "app.kubernetes.io/part-of" = "backd"
      # The workloads run with restricted pod security.
      "pod-security.kubernetes.io/enforce" = "restricted"
    }
  }
}

# ---------------------------------------------------------------- generated secrets
# Rotate one with `terraform apply -replace=random_password.<name>`; the README says which pods
# restart and what BACKD_SECRETS_KEY needs first.

resource "random_password" "secrets_key" {
  length  = 48
  special = false
}

resource "random_password" "executor_token" {
  length  = 48
  special = false
}

resource "random_password" "callback_key" {
  length  = 48
  special = false
}

resource "random_password" "egress_key" {
  length  = 48
  special = false
}

resource "random_password" "metrics_token" {
  length  = 48
  special = false
}

# ---------------------------------------------------------------- the Secrets

resource "kubernetes_secret" "mongo" {
  metadata {
    name      = "backd-mongo"
    namespace = kubernetes_namespace.backd.metadata[0].name
  }

  data = {
    MONGO_URI       = local.mongo_uri
    MONGO_ADMIN_URI = local.mongo_admin_uri
    "ca.pem"        = local.mongo_ca
  }
}

resource "kubernetes_secret" "keys" {
  metadata {
    name      = "backd-keys"
    namespace = kubernetes_namespace.backd.metadata[0].name
  }

  data = {
    BACKD_SECRETS_KEY    = random_password.secrets_key.result
    BACKD_EXECUTOR_TOKEN = random_password.executor_token.result
    BACKD_CALLBACK_KEY   = random_password.callback_key.result
    BACKD_EGRESS_KEY     = random_password.egress_key.result
    BACKD_METRICS_TOKEN  = random_password.metrics_token.result
  }
}

resource "kubernetes_secret" "registry" {
  metadata {
    name      = "backd-registry"
    namespace = kubernetes_namespace.backd.metadata[0].name
  }

  type = "kubernetes.io/dockerconfigjson"

  data = {
    ".dockerconfigjson" = digitalocean_container_registry_docker_credentials.pull.docker_credentials
  }
}

resource "kubernetes_config_map" "install" {
  metadata {
    name      = "backd-install"
    namespace = kubernetes_namespace.backd.metadata[0].name
  }

  data = merge(
    {
      BACKD_URL           = "https://${var.api_host}"
      BACKD_ENV           = var.environment
      BACKD_DEPLOYMENT_ID = var.install
      API_HOST            = var.api_host
      ADMIN_HOST          = var.admin_host
      CLUSTER_ISSUER      = local.cluster_issuer
      REGISTRY_ENDPOINT   = digitalocean_container_registry.main.endpoint
      SPACES_ENDPOINT     = local.spaces_url
      SPACES_REGION       = local.spaces_region
      PRIVATE_BUCKET      = digitalocean_spaces_bucket.private.name
      DATABASE_CIDR       = digitalocean_vpc.main.ip_range
    },
    local.has_public ? {
      PUBLIC_BUCKET = var.public_bucket_name
      PUBLIC_URL    = local.public_url
    } : {},
  )
}
