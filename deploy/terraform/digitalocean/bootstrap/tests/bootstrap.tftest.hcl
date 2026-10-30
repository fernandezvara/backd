# Offline: the provider is mocked, nothing is created.
mock_provider "digitalocean" {}

variables {
  state_bucket_name = "acme-backd-state"
}

run "state_bucket_is_private_and_versioned" {
  command = plan

  assert {
    condition     = digitalocean_spaces_bucket.state.acl == "private"
    error_message = "the state bucket must be private"
  }

  assert {
    condition     = digitalocean_spaces_bucket.state.versioning[0].enabled
    error_message = "the state bucket must be versioned"
  }

  assert {
    condition     = !digitalocean_spaces_bucket.state.force_destroy
    error_message = "the state bucket must not be force-destroyed"
  }
}

run "key_is_bound_to_the_bucket" {
  command = plan

  assert {
    condition     = length(digitalocean_spaces_key.state.grant) == 1 && digitalocean_spaces_key.state.grant[0].permission == "fullaccess"
    error_message = "the state key has one grant: the state bucket"
  }
}

run "bucket_name_is_validated" {
  command = plan

  variables {
    state_bucket_name = "Not_Valid"
  }

  expect_failures = [var.state_bucket_name]
}

run "region_is_validated" {
  command = plan

  variables {
    region = "mars1"
  }

  expect_failures = [var.region]
}
