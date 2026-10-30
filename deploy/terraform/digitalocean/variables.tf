# ---------------------------------------------------------------- the installation

variable "install" {
  description = "Names this installation (1 to 24 characters: lower-case letters, digits and hyphens, starting with a letter). It prefixes every resource and is its tag, so several installations can share an account."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{0,22}[a-z0-9]$", var.install)) || can(regex("^[a-z]$", var.install))
    error_message = "install must be 1 to 24 characters: lower-case letters, digits and hyphens, starting with a letter and not ending with a hyphen."
  }
}

variable "region" {
  description = "Region of the cluster, the database and the load balancer (for example fra1, ams3, nyc3, sfo3, sgp1, syd1, lon1)."
  type        = string
  default     = "fra1"
}

# ---------------------------------------------------------------- network

variable "vpc_ip_range" {
  description = "Private network of the installation. It must not overlap the cluster's pods (10.244.0.0/16) or services (10.245.0.0/16)."
  type        = string
  default     = "10.30.0.0/16"

  validation {
    condition     = can(cidrhost(var.vpc_ip_range, 0))
    error_message = "vpc_ip_range must be a CIDR block such as 10.30.0.0/16."
  }
}

# ---------------------------------------------------------------- the Kubernetes cluster

variable "kubernetes_version_prefix" {
  description = "Minor version of Kubernetes, for example \"1.33.\" (the latest patch of it is used). Unset: the latest version DigitalOcean offers."
  type        = string
  default     = null
}

variable "node_size" {
  description = "Size slug of the worker nodes (`doctl kubernetes options sizes`). The default is 2 vCPU and 4 GB; s-4vcpu-8gb or g-2vcpu-8gb (dedicated) are the usual steps up."
  type        = string
  default     = "s-2vcpu-4gb"
}

variable "node_count" {
  description = "Number of worker nodes (ignored when autoscaling is set: the cluster starts at node_min and moves within the range)."
  type        = number
  default     = 2

  validation {
    condition     = var.node_count >= 1 && var.node_count <= 512 && floor(var.node_count) == var.node_count
    error_message = "node_count must be a whole number from 1 to 512."
  }
}

variable "node_min" {
  description = "With node_max: the cluster autoscales between the two. Both or neither."
  type        = number
  default     = null
}

variable "node_max" {
  description = "With node_min: the cluster autoscales between the two. Both or neither."
  type        = number
  default     = null

  validation {
    # A conditional, not && and ||: older Terraform evaluates both sides of those, and compares null.
    condition     = var.node_min == null ? var.node_max == null : (var.node_max == null ? false : (var.node_min >= 1 && var.node_min <= var.node_max))
    error_message = "node_min and node_max are set together, and 1 <= node_min <= node_max."
  }
}

variable "ha_control_plane" {
  description = "A highly available control plane (three replicas; about $40 a month). Not needed for two nodes."
  type        = bool
  default     = false
}

variable "kubernetes_auto_upgrade" {
  description = "Let DigitalOcean upgrade the cluster to new patch versions in the maintenance window."
  type        = bool
  default     = true
}

variable "maintenance_day" {
  description = "Day of the maintenance window (monday ... sunday, or any)."
  type        = string
  default     = "sunday"
}

variable "maintenance_start" {
  description = "Start of the maintenance window, UTC, HH:MM."
  type        = string
  default     = "03:00"
}

variable "control_plane_allowed_cidrs" {
  description = "Addresses allowed to reach the Kubernetes API (CIDRs). Empty: the API is reachable from anywhere with the cluster's credentials. Set it to your own addresses (and your pipeline's) once you know them."
  type        = list(string)
  default     = []
}

# ---------------------------------------------------------------- credentials

variable "do_token" {
  description = "DigitalOcean API token. Leave it unset and use DIGITALOCEAN_TOKEN."
  type        = string
  default     = null
  sensitive   = true
}

variable "spaces_access_id" {
  description = "Spaces key that can create buckets. Leave it unset and use SPACES_ACCESS_KEY_ID."
  type        = string
  default     = null
  sensitive   = true
}

variable "spaces_secret_key" {
  description = "Secret of that Spaces key. Leave it unset and use SPACES_SECRET_ACCESS_KEY."
  type        = string
  default     = null
  sensitive   = true
}

# ---------------------------------------------------------------- the managed MongoDB

variable "database_nodes" {
  description = "1 (one node: no failover, and a maintenance window is downtime) or 3 (a primary and two standby nodes). DigitalOcean offers nothing in between. Going from 1 to 3 later is a change of this value."
  type        = number
  default     = 1

  validation {
    condition     = contains([1, 3], var.database_nodes)
    error_message = "database_nodes must be 1 (one node) or 3 (a primary and two standby nodes): DigitalOcean's MongoDB has no other size."
  }
}

variable "database_size" {
  description = "Size slug of each database node: db-s-1vcpu-1gb, db-s-1vcpu-2gb, db-s-2vcpu-4gb, db-s-4vcpu-8gb, db-s-6vcpu-16gb, db-s-8vcpu-32gb or db-s-16vcpu-64gb. Three nodes cost three times one."
  type        = string
  default     = "db-s-1vcpu-2gb"

  validation {
    condition     = can(regex("^db-s-[0-9]+vcpu-[0-9]+gb$", var.database_size))
    error_message = "database_size must be a managed database size slug such as db-s-1vcpu-2gb."
  }
}

variable "database_version" {
  description = "MongoDB major version (the versions DigitalOcean offers are in the control panel and in `doctl databases options versions mongodb`). It can only go up: DigitalOcean upgrades a cluster one major version at a time and never downgrades."
  type        = string
  default     = "8"
}

variable "database_storage_mib" {
  description = "Disk of each node in MiB. Unset: the size's default."
  type        = number
  default     = null
}

variable "database_maintenance_day" {
  description = "Day of the database maintenance window (monday ... sunday)."
  type        = string
  default     = "sunday"
}

variable "database_maintenance_hour" {
  description = "Hour (UTC, HH:MM:SS) the database maintenance window starts."
  type        = string
  default     = "03:00:00"
}

variable "database_ca" {
  description = "Optional CA certificate (PEM) the pods verify the managed MongoDB against, in place of the system roots. DigitalOcean's MongoDB presents a Let's Encrypt certificate, so leave it unset."
  type        = string
  default     = null

  validation {
    condition     = var.database_ca == null ? true : strcontains(var.database_ca, "BEGIN CERTIFICATE")
    error_message = "database_ca must be a PEM certificate (the PEM text, starting with -----BEGIN CERTIFICATE-----)."
  }
}

variable "database_allowed_cidrs" {
  description = "Addresses besides the cluster that may connect to the database (for example your own, to restore a backup). Empty: the cluster only."
  type        = list(string)
  default     = []
}
