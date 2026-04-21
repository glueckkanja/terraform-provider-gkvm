---
page_title: "Provider: GKVM"
description: |-
  The GKVM provider reads content from a GitHub repository for glueckkanja verified modules.
---

# GKVM Provider

The GKVM provider reads content from a GitHub repository for glueckkanja verified modules (GKVM). It exposes repository content as Terraform data sources, starting with monitoring alert profiles.

## Example Usage

```hcl
provider "gkvm" {
  github_repo = "glueckkanja/gkvm-monitoring-defaults"
  github_ref  = "main"
}
```

## Authentication

The provider resolves a GitHub token in the following order:

1. `github_token` attribute in the provider block
2. `GITHUB_TOKEN` environment variable
3. `GH_TOKEN` environment variable
4. `gh auth token` CLI output (if the `gh` CLI is authenticated)

For CI/CD pipelines, set `GITHUB_TOKEN` as an environment variable or pass `github_token` explicitly. For local development, authenticating with the `gh` CLI (`gh auth login`) is sufficient.

The token requires `contents: read` permission on the target repository.

## Schema

### Required

- `github_repo` (String) GitHub repository to read from, in `owner/repo` format (e.g., `"glueckkanja/gkvm-monitoring-defaults"`).

### Optional

- `github_ref` (String) Git ref to fetch — branch, tag, or commit SHA. Defaults to `"main"`.
- `github_token` (String, Sensitive) GitHub personal access token. Prefer environment variables or the `gh` CLI over setting this explicitly.
