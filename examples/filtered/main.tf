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
  github_ref  = "v1.0.0"
}

# Fetch only specific profiles from a custom directory path
data "gkvm_monitoring_profiles" "selected" {
  profile_path = "monitoring/v2"
  filter       = "application_insights,virtual_machine"
}

output "selected_profile_names" {
  description = "Profile names returned after filtering."
  value       = keys(data.gkvm_monitoring_profiles.selected.profiles)
}

output "vm_metric_alerts" {
  description = "Metric alert definitions for the virtual_machine profile."
  value       = jsondecode(data.gkvm_monitoring_profiles.selected.profiles["virtual_machine"]).metric_alerts
}
