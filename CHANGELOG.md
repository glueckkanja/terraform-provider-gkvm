# Changelog

## [0.1.0] — Initial Release

### Overview

First public release of the `glueckkanja/gkvm` Terraform/OpenTofu provider. Reads content from a GitHub repository and exposes it as Terraform data sources — starting with monitoring alert profiles for GKVM modules.

---

### Provider: `gkvm`

Connects to a GitHub repository via the GitHub REST API.

```hcl
provider "gkvm" {
  github_repo = "glueckkanja/gkvm-monitoring-defaults"
  github_ref  = "main"
}
```

#### Schema

| Attribute | Required | Description |
|-----------|----------|-------------|
| `github_repo` | Yes | Repository in `owner/repo` format |
| `github_ref` | No | Branch, tag, or commit SHA. Defaults to `"main"` |
| `github_token` | No | GitHub PAT (sensitive). Prefer env vars or `gh` CLI |

#### Authentication

Token is resolved in the following order:

1. `github_token` provider attribute
2. `GITHUB_TOKEN` environment variable
3. `GH_TOKEN` environment variable
4. `gh auth token` CLI output

Requires `contents: read` permission on the target repository. For local development, `gh auth login` is sufficient.

---

### Data Source: `gkvm_monitoring_profiles`

Fetches monitoring alert profiles from a directory of YAML files in the configured repository. Each profile is returned as a JSON string containing `metric_alerts` and `log_alerts`. Use `jsondecode()` to consume them in Terraform.

#### Schema

| Attribute | Type | Description |
|-----------|------|-------------|
| `profile_path` | Optional String | Directory path within the repo. Defaults to `"defaults"` |
| `filter` | Optional String | Comma-separated list of profile names to include in `profiles` |
| `profiles` | Map of String (read-only) | Profile name → JSON string `{ metric_alerts, log_alerts }` |
| `profile_names` | List of String (read-only) | Sorted list of all available profiles. Not affected by `filter` |

#### Example — all profiles

```hcl
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
}

data "gkvm_monitoring_profiles" "defaults" {}

output "available_profiles" {
  value = data.gkvm_monitoring_profiles.defaults.profile_names
}

output "application_insights_alerts" {
  value = jsondecode(data.gkvm_monitoring_profiles.defaults.profiles["application_insights"])
}
```

#### Example — filtered selection from a custom path

```hcl
data "gkvm_monitoring_profiles" "selected" {
  profile_path = "monitoring/v2"
  filter       = "application_insights,virtual_machine"
}
```

---

### Requirements

- Terraform >= 1.11 **or** OpenTofu >= 1.11
- GitHub token with `contents: read` on the target repository
