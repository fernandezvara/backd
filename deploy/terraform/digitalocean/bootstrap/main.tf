# The private Space that holds the state of the main configuration, and a key that can use
# that Space and nothing else. Applied once, with local state: keep that small state file (it
# holds the key's secret) somewhere safe, or destroy the key and make a new one when needed.

resource "digitalocean_spaces_bucket" "state" {
  name   = var.state_bucket_name
  region = var.region
  acl    = "private"

  versioning {
    enabled = var.versioning
  }

  # The state is the map of everything else: Terraform never deletes a bucket that holds it.
  force_destroy = false
}

resource "digitalocean_spaces_key" "state" {
  name = "${var.state_bucket_name}-terraform"

  # DigitalOcean accepts "fullaccess" only on a key for every bucket (an empty bucket name), so a
  # key bound to this one bucket is "readwrite": get, put, delete and list, which is all the
  # state backend does.
  grant {
    bucket     = digitalocean_spaces_bucket.state.name
    permission = "readwrite"
  }
}
