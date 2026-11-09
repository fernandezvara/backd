# The state lives in the private Space the bootstrap configuration made (see bootstrap/ and
# the README). The settings come from the file it prints, so nothing here depends on one
# account:
#
#   terraform init -backend-config=backend.hcl
#
# and the state key from the environment (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY).
terraform {
  backend "s3" {}
}
