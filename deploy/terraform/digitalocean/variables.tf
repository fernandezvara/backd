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
  description = "MongoDB major version (the versions DigitalOcean offers are in the control panel and in `doctl databases options versions mongodb`)."
  type        = string
  default     = "7"
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

variable "database_allowed_cidrs" {
  description = "Addresses besides the cluster that may connect to the database (for example your own, to restore a backup). Empty: the cluster only."
  type        = list(string)
  default     = []
}

# ---------------------------------------------------------------- Spaces (the files)

variable "buckets" {
  description = "`private` makes one Space for the private files; `private_and_public` makes a second one for public files (a file field with public: true), readable by anyone who has an address and never listable."
  type        = string
  default     = "private"

  validation {
    condition     = contains(["private", "private_and_public"], var.buckets)
    error_message = "buckets must be \"private\" (one Space) or \"private_and_public\" (two)."
  }
}

variable "private_bucket_name" {
  description = "Name of the Space for the private files (3 to 63 characters: lower-case letters, digits and hyphens). Names are unique per region across all accounts."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$", var.private_bucket_name))
    error_message = "private_bucket_name must be 3 to 63 characters: lower-case letters, digits and hyphens, not starting or ending with a hyphen."
  }
}

variable "public_bucket_name" {
  description = "Name of the Space for the public files. Required when buckets is private_and_public; it is part of the address of every public file unless a CDN with assets_host is used."
  type        = string
  default     = ""

  validation {
    condition     = var.public_bucket_name == "" || can(regex("^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$", var.public_bucket_name))
    error_message = "public_bucket_name must be 3 to 63 characters: lower-case letters, digits and hyphens, not starting or ending with a hyphen."
  }

  validation {
    condition     = var.buckets != "private_and_public" || var.public_bucket_name != ""
    error_message = "public_bucket_name is required when buckets is \"private_and_public\"."
  }

  validation {
    condition     = var.public_bucket_name == "" || var.public_bucket_name != var.private_bucket_name
    error_message = "the public and the private Space need different names."
  }
}

variable "spaces_region" {
  description = "Region of the Spaces. Unset: the region of the installation."
  type        = string
  default     = null
}

variable "public_cdn" {
  description = "Put DigitalOcean's CDN in front of the public Space. With assets_host the files are served from that name over HTTPS (a certificate is issued); without it, from the CDN's own address."
  type        = bool
  default     = false
}

variable "assets_host" {
  description = "Hostname for the public files through the CDN (for example assets.example.com), in a domain that is on DigitalOcean DNS. Needs public_cdn."
  type        = string
  default     = ""

  validation {
    condition     = var.assets_host == "" || var.public_cdn
    error_message = "assets_host needs public_cdn = true."
  }
}

variable "private_cors_origins" {
  description = "Origins of the browsers that upload straight to the private Space or read from it (direct uploads, signed downloads): for example [\"https://app.example.com\"]. Empty: no CORS rule, so only uploads through backd work."
  type        = list(string)
  default     = []
}

variable "public_cors_origins" {
  description = "Origins that may read the public Space from a browser. The default lets any page show the files."
  type        = list(string)
  default     = ["*"]
}

variable "spaces_key_permission" {
  description = "What each Space's key may do on its Space: read, readwrite or fullaccess. backd needs to put, get, head, delete and list, which `readwrite` gives."
  type        = string
  default     = "readwrite"

  validation {
    condition     = contains(["read", "readwrite", "fullaccess"], var.spaces_key_permission)
    error_message = "spaces_key_permission must be read, readwrite or fullaccess."
  }
}

variable "abort_incomplete_uploads_days" {
  description = "Days after which an unfinished multipart upload is removed, so abandoned parts don't cost storage."
  type        = number
  default     = 7
}

# ---------------------------------------------------------------- the container registry

variable "registry_tier" {
  description = "starter (one repository, 500 MiB, free), basic (five repositories, 5 GiB, about $5 a month) or professional (unlimited repositories, 100 GiB, about $20). One repository is enough for one installation: releases are tags of it."
  type        = string
  default     = "basic"

  validation {
    condition     = contains(["starter", "basic", "professional"], var.registry_tier)
    error_message = "registry_tier must be starter, basic or professional."
  }
}

variable "registry_name" {
  description = "Name of the registry (unique across DigitalOcean: 1 to 63 characters, lower-case letters, digits and hyphens). Unset: the name of the installation."
  type        = string
  default     = null

  validation {
    condition     = var.registry_name == null || can(regex("^[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$", var.registry_name))
    error_message = "registry_name must be 2 to 63 characters: lower-case letters, digits and hyphens, not starting or ending with a hyphen."
  }
}

variable "registry_region" {
  description = "Region of the registry (nyc3, sfo3, ams3, sgp1, fra1, syd1). Unset: the region of the installation."
  type        = string
  default     = null
}

variable "registry_push_credential_days" {
  description = "Days the credential the pipeline pushes with stays valid. 0: the provider's default (practically no expiry). Replace it with `terraform apply -replace=digitalocean_container_registry_docker_credentials.push`."
  type        = number
  default     = 0

  validation {
    condition     = var.registry_push_credential_days >= 0 && floor(var.registry_push_credential_days) == var.registry_push_credential_days
    error_message = "registry_push_credential_days is a whole number of days, 0 or more."
  }
}

# ---------------------------------------------------------------- traffic: load balancer and DNS

variable "domain" {
  description = "The domain the installation's names are in. It must already exist on DigitalOcean DNS (Networking > Domains) and its name servers must point there."
  type        = string

  validation {
    condition     = can(regex("^([a-z0-9]([a-z0-9-]*[a-z0-9])?\\.)+[a-z]{2,}$", var.domain))
    error_message = "domain must be a domain name such as example.com."
  }
}

variable "api_host" {
  description = "Hostname of the public API (for example api.example.com), in `domain`. It is backd's BACKD_URL."
  type        = string

  validation {
    condition     = can(regex("^([a-z0-9]([a-z0-9-]*[a-z0-9])?\\.)+[a-z]{2,}$", var.api_host))
    error_message = "api_host must be a hostname such as api.example.com."
  }

  validation {
    condition     = var.api_host == var.domain || endswith(var.api_host, ".${var.domain}")
    error_message = "api_host must be the domain or a name inside it."
  }
}

variable "admin_host" {
  description = "Hostname of the administration API and interface (for example admin.example.com), in `domain`. It is reachable from admin_allowed_cidrs only."
  type        = string

  validation {
    condition     = can(regex("^([a-z0-9]([a-z0-9-]*[a-z0-9])?\\.)+[a-z]{2,}$", var.admin_host))
    error_message = "admin_host must be a hostname such as admin.example.com."
  }

  validation {
    condition     = var.admin_host == var.domain || endswith(var.admin_host, ".${var.domain}")
    error_message = "admin_host must be the domain or a name inside it."
  }

  validation {
    condition     = var.admin_host != var.api_host
    error_message = "admin_host and api_host must be different names."
  }
}

variable "dns_ttl" {
  description = "TTL, in seconds, of the records the installation creates."
  type        = number
  default     = 300
}

variable "lb_size" {
  description = "Size of the load balancer: lb-small (10,000 simultaneous connections), lb-medium (50,000) or lb-large (100,000). Each is a different price."
  type        = string
  default     = "lb-small"

  validation {
    condition     = contains(["lb-small", "lb-medium", "lb-large"], var.lb_size)
    error_message = "lb_size must be lb-small, lb-medium or lb-large."
  }
}

variable "lb_algorithm" {
  description = "How the load balancer picks a node: round_robin or least_connections (better for long-lived realtime streams)."
  type        = string
  default     = "round_robin"

  validation {
    condition     = contains(["round_robin", "least_connections"], var.lb_algorithm)
    error_message = "lb_algorithm must be round_robin or least_connections."
  }
}

variable "ingress_replicas" {
  description = "Pods of the ingress controller. Two, on different nodes, survive the loss of one."
  type        = number
  default     = 2

  validation {
    condition     = var.ingress_replicas >= 1 && floor(var.ingress_replicas) == var.ingress_replicas
    error_message = "ingress_replicas must be a whole number, 1 or more."
  }
}

variable "traefik_chart_version" {
  description = "Version of the Traefik Helm chart (https://traefik.github.io/charts)."
  type        = string
  default     = "42.1.3"
}
