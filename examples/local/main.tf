# Read the profiles from a directory instead of the API.
#
# Useful where one run reads the profiles many times over: a CI job fetches the
# repository once with git and every reader is served from that directory, with
# no requests and no token.

terraform {
  required_providers {
    gkvm = {
      source = "glueckkanja/gkvm"
    }
  }
}

provider "gkvm" {
  platform  = "local"
  local_dir = "/srv/bundle/gkvm-monitoring-defaults"
}

data "gkvm_monitoring_profiles" "this" {}

output "profile_names" {
  value = data.gkvm_monitoring_profiles.this.profile_names
}
