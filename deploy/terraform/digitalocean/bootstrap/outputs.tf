output "bucket" {
  description = "The state bucket."
  value       = digitalocean_spaces_bucket.state.name
}

output "endpoint" {
  description = "The Spaces endpoint of the state bucket."
  value       = "https://${var.region}.digitaloceanspaces.com"
}

output "backend_config" {
  description = "The partial backend configuration: save it as ../backend.hcl and run `terraform init -backend-config=backend.hcl` in the main configuration. It holds no secret."
  value       = <<-EOT
    bucket                      = "${digitalocean_spaces_bucket.state.name}"
    key                         = "backd/terraform.tfstate"
    region                      = "us-east-1"
    endpoints                   = { s3 = "https://${var.region}.digitaloceanspaces.com" }
    skip_credentials_validation = true
    skip_requesting_account_id  = true
    skip_metadata_api_check     = true
    skip_region_validation      = true
    skip_s3_checksum            = true
  EOT
}

output "access_key_id" {
  description = "The state key's id: export it as AWS_ACCESS_KEY_ID before `terraform init` in the main configuration."
  value       = digitalocean_spaces_key.state.access_key
  sensitive   = true
}

output "secret_access_key" {
  description = "The state key's secret: export it as AWS_SECRET_ACCESS_KEY."
  value       = digitalocean_spaces_key.state.secret_key
  sensitive   = true
}
