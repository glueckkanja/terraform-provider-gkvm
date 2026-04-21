---
page_title: "gkvm_monitoring_profiles Data Source - gkvm"
description: |-
  Fetches monitoring alert profiles from a directory of YAML files in the configured GitHub repository.
---

# gkvm_monitoring_profiles (Data Source)

Fetches monitoring alert profiles from a directory of YAML files in the configured GitHub repository. Each profile is returned as a JSON string containing `metric_alerts` and `log_alerts`. Use `jsondecode()` to consume them in Terraform.

## Example Usage

### All profiles with default path

```hcl
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

### Filtered selection from a custom path

```hcl
data "gkvm_monitoring_profiles" "selected" {
  profile_path = "monitoring/v2"
  filter       = "application_insights,virtual_machine"
}
```

## Schema

### Optional

- `profile_path` (String) Directory path within the repository containing YAML profile files. Defaults to `"defaults"`.
- `filter` (String) Comma-separated list of profile names to include in `profiles`. If omitted, all profiles are returned. `profile_names` is always the full list regardless of this setting.

### Read-Only

- `profiles` (Map of String) Map of profile name to JSON string. Each value decodes to `{ metric_alerts: {...}, log_alerts: {...} }`.
- `profile_names` (List of String) Sorted list of all available profile names in the directory. Not affected by `filter`.
