terraform {
  required_providers {
    gkvm = {
      source  = "glueckkanja/gkvm"
      version = "~> 0.2"
    }
  }
}

# GitLab self-managed: point base_url at the instance and the /api/v4 suffix is
# added for you. Omit base_url entirely for GitLab.com.
provider "gkvm" {
  platform   = "gitlab"
  base_url   = "https://gitlab.example.com"
  repository = "my-group/platform/gkvm-monitoring-defaults"
  ref        = "main"

  # Token resolution, in order: token, GKVM_TOKEN, GITLAB_TOKEN, CI_JOB_TOKEN,
  # then "glab auth token --hostname gitlab.example.com".
  # The token needs the read_api scope and at least the Reporter role.
  # A CI_JOB_TOKEN is sent as JOB-TOKEN, which is the header GitLab accepts it in.
}

data "gkvm_monitoring_profiles" "all" {}

output "available_profiles" {
  description = "All profile names discovered in the project."
  value       = data.gkvm_monitoring_profiles.all.profile_names
}
