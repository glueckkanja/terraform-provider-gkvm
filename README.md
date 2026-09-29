# terraform-provider-gkvm

Terraform/OpenTofu provider for glueckkanja verified modules (GKVM). Reads content from a Git repository and exposes it as Terraform data sources.

Supported platforms: **GitHub** (GitHub.com, Enterprise Cloud including data residency, Enterprise Server) and **GitLab** (GitLab.com, self-managed, dedicated). The API endpoint is configurable, so self-hosted and enterprise deployments are reached the same way as the public SaaS.

## Requirements

- [Terraform](https://developer.hashicorp.com/terraform/downloads) >= 1.11 or [OpenTofu](https://opentofu.org/docs/intro/install/) >= 1.11
- [Go](https://golang.org/doc/install) >= 1.23 (for building from source)

## Usage

```hcl
terraform {
  required_providers {
    gkvm = {
      source  = "glueckkanja/gkvm"
      version = "~> 0.2"
    }
  }
}

provider "gkvm" {
  repository = "glueckkanja/gkvm-monitoring-defaults"
  ref        = "main"
}

data "gkvm_monitoring_profiles" "defaults" {}
```

Against a self-hosted or enterprise deployment, add the API endpoint:

```hcl
# GitHub Enterprise Server (the /api/v3 suffix is added for you)
provider "gkvm" {
  base_url   = "https://github.example.com"
  repository = "my-org/gkvm-monitoring-defaults"
}

# GitLab self-managed (the /api/v4 suffix is added for you)
provider "gkvm" {
  platform   = "gitlab"
  base_url   = "https://gitlab.example.com"
  repository = "my-group/platform/gkvm-monitoring-defaults"
}
```

The token is resolved automatically: `GKVM_TOKEN`, then `GH_TOKEN` / `GITHUB_TOKEN` (GitHub) or `GITLAB_TOKEN` (GitLab), then the platform CLI (`gh auth token` / `glab auth token`) for the host in `base_url`. Set `token` in the provider block only if none of those is available.

Every request stays on the host in `base_url` — no second hostname has to be reachable. See [the provider documentation](docs/index.md) for endpoints, permissions and the migration from v0.1.x attribute names.

## Data Sources

| Name | Description |
|------|-------------|
| [`gkvm_monitoring_profiles`](docs/data-sources/monitoring_profiles.md) | Fetches monitoring alert profiles from a YAML directory in the repository |

## Development

### Build

```shell
go build ./...
```

### Test

```shell
go test ./...
```

### Install locally

```shell
go install .
```

This places the binary in `$GOPATH/bin`. To use it with Terraform/OpenTofu locally, set up a [development override](https://developer.hashicorp.com/terraform/cli/config/config-file#development-overrides-for-provider-developers) in your `.terraformrc`:

```hcl
provider_installation {
  dev_overrides {
    "glueckkanja/gkvm" = "/path/to/your/GOPATH/bin"
  }
  direct {}
}
```
