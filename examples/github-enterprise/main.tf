terraform {
  required_providers {
    gkvm = {
      source  = "glueckkanja/gkvm"
      version = "~> 0.2"
    }
  }
}

# GitHub Enterprise Server: point base_url at the instance and the /api/v3
# suffix is added for you. An endpoint that already carries a path is used
# verbatim, so "https://github.example.com/api/v3" works too.
provider "gkvm" {
  base_url   = "https://github.example.com"
  repository = "my-org/gkvm-monitoring-defaults"
  ref        = "main"

  # Token resolution, in order: token, GKVM_TOKEN, GH_TOKEN, GITHUB_TOKEN,
  # then "gh auth token --hostname github.example.com".
  # The token needs "contents: read" on the repository.
}

# GitHub Enterprise Cloud with data residency publishes the API on an "api."
# hostname under the tenant subdomain:
#
# provider "gkvm" {
#   base_url   = "https://api.SUBDOMAIN.ghe.com"
#   repository = "my-org/gkvm-monitoring-defaults"
# }

data "gkvm_monitoring_profiles" "all" {}

output "available_profiles" {
  description = "All profile names discovered in the repository."
  value       = data.gkvm_monitoring_profiles.all.profile_names
}
