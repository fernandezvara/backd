locals {
  spaces_region = coalesce(var.spaces_region, var.region)
  has_public    = var.buckets == "private_and_public"
  spaces_url    = "https://${local.spaces_region}.digitaloceanspaces.com"
}

# ---------------------------------------------------------------- the private Space

resource "digitalocean_spaces_bucket" "private" {
  name   = var.private_bucket_name
  region = local.spaces_region
  acl    = "private"

  lifecycle_rule {
    id                                     = "abort-incomplete-uploads"
    enabled                                = true
    abort_incomplete_multipart_upload_days = var.abort_incomplete_uploads_days
  }

  # The files are the data: Terraform never empties a Space to delete it.
  force_destroy = false
}

# Browsers upload straight to the Space and download from signed links (docs: Files ->
# Transfers). The headers are the ones backd's direct uploads send.
resource "digitalocean_spaces_bucket_cors_configuration" "private" {
  count  = length(var.private_cors_origins) > 0 ? 1 : 0
  bucket = digitalocean_spaces_bucket.private.name
  region = local.spaces_region

  cors_rule {
    allowed_origins = var.private_cors_origins
    allowed_methods = ["GET", "PUT", "HEAD"]
    allowed_headers = ["Content-Type", "x-amz-checksum-sha256"]
    expose_headers  = ["ETag"]
    max_age_seconds = 3600
  }
}

# One key per Space, so the key of the private files can't touch the public ones and the other
# way round.
resource "digitalocean_spaces_key" "private" {
  name = "${var.install}-private-files"

  grant {
    bucket     = digitalocean_spaces_bucket.private.name
    permission = var.spaces_key_permission
  }
}

# ---------------------------------------------------------------- the public Space

# Not "public-read": that ACL lets anyone list the Space. The policy below allows reading an
# object whose address is known and nothing else.
resource "digitalocean_spaces_bucket" "public" {
  count  = local.has_public ? 1 : 0
  name   = var.public_bucket_name
  region = local.spaces_region
  acl    = "private"

  lifecycle_rule {
    id                                     = "abort-incomplete-uploads"
    enabled                                = true
    abort_incomplete_multipart_upload_days = var.abort_incomplete_uploads_days
  }

  force_destroy = false
}

resource "digitalocean_spaces_bucket_policy" "public" {
  count  = local.has_public ? 1 : 0
  bucket = digitalocean_spaces_bucket.public[0].name
  region = local.spaces_region

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "PublicRead"
      Effect    = "Allow"
      Principal = "*"
      Action    = ["s3:GetObject"]
      Resource  = ["arn:aws:s3:::${var.public_bucket_name}/*"]
    }]
  })
}

resource "digitalocean_spaces_bucket_cors_configuration" "public" {
  count  = local.has_public ? 1 : 0
  bucket = digitalocean_spaces_bucket.public[0].name
  region = local.spaces_region

  cors_rule {
    allowed_origins = var.public_cors_origins
    allowed_methods = ["GET", "HEAD"]
    max_age_seconds = 86400
  }
}

resource "digitalocean_spaces_key" "public" {
  count = local.has_public ? 1 : 0
  name  = "${var.install}-public-files"

  grant {
    bucket     = digitalocean_spaces_bucket.public[0].name
    permission = var.spaces_key_permission
  }
}

# ---------------------------------------------------------------- the CDN in front of it

resource "digitalocean_certificate" "assets" {
  count   = local.has_public && var.public_cdn && var.assets_host != "" ? 1 : 0
  name    = "${var.install}-assets"
  type    = "lets_encrypt"
  domains = [var.assets_host]

  lifecycle {
    create_before_destroy = true
  }
}

resource "digitalocean_cdn" "public" {
  count  = local.has_public && var.public_cdn ? 1 : 0
  origin = digitalocean_spaces_bucket.public[0].bucket_domain_name

  custom_domain    = var.assets_host != "" ? var.assets_host : null
  certificate_name = var.assets_host != "" ? digitalocean_certificate.assets[0].name : null
  ttl              = 3600
}

locals {
  # The address browsers read public files at (public_storage.url): the host chosen, else the
  # CDN's own address, else the Space's.
  public_url = !local.has_public ? null : (
    !var.public_cdn ? "https://${var.public_bucket_name}.${local.spaces_region}.digitaloceanspaces.com" : (
      var.assets_host != "" ? "https://${var.assets_host}" : "https://${digitalocean_cdn.public[0].endpoint}"
    )
  )
}
