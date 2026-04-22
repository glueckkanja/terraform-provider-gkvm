terraform {
  required_providers {
    gkvm = {
      source  = "glueckkanja/gkvm"
      version = "~> 0.1"
    }
  }
}

provider "gkvm" {
  github_repo = "glueckkanja/gkvm-monitoring-defaults"
  # github_ref defaults to "main"
  # github_token resolved automatically from GITHUB_TOKEN / GH_TOKEN / gh CLI
}

# Fetch all monitoring profiles from the default path ("defaults/")
data "gkvm_monitoring_profiles" "all" {}

output "available_profiles" {
  description = "All profile names discovered in the repository."
  value       = data.gkvm_monitoring_profiles.all.profile_names
}

output "application_insights_alerts" {
  description = "Decoded alert definitions for the application_insights profile."
  value       = jsondecode(data.gkvm_monitoring_profiles.all.profiles["application_insights"])
}
