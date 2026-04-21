# terraform-provider-gkvm

Terraform/OpenTofu provider for glueckkanja verified modules (GKVM). Reads content from a GitHub repository and exposes it as Terraform data sources.

## Requirements

- [Terraform](https://developer.hashicorp.com/terraform/downloads) >= 1.11 or [OpenTofu](https://opentofu.org/docs/intro/install/) >= 1.11
- [Go](https://golang.org/doc/install) >= 1.23 (for building from source)

## Usage

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
  github_ref  = "main"
}

data "gkvm_monitoring_profiles" "defaults" {}
```

The provider resolves a GitHub token automatically from `GITHUB_TOKEN` / `GH_TOKEN` environment variables or the `gh` CLI. Set `github_token` in the provider block only if neither is available.

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
