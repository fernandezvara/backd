variable "state_bucket_name" {
  description = "Name of the private Space that holds the Terraform state (3 to 63 characters: lower-case letters, digits and hyphens). Spaces names are unique per region."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$", var.state_bucket_name))
    error_message = "state_bucket_name must be 3 to 63 characters: lower-case letters, digits and hyphens, not starting or ending with a hyphen."
  }
}

variable "region" {
  description = "Spaces region of the state bucket (for example fra1, ams3, nyc3, sfo3, sgp1, syd1)."
  type        = string
  default     = "fra1"

  validation {
    condition     = contains(["nyc3", "sfo2", "sfo3", "ams3", "sgp1", "fra1", "syd1", "lon1", "blr1", "tor1", "atl1"], var.region)
    error_message = "region is not a Spaces region this configuration knows."
  }
}

variable "versioning" {
  description = "Keep previous versions of the state file, so a bad apply can be undone. Leave it on."
  type        = bool
  default     = true
}

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
