---
page_title: "Provider: gkvm"
description: |-
  Reads content from a Git repository for glueckkanja verified modules (GKVM). Supports GitHub and GitLab, including self-hosted and enterprise deployments.
---

# GKVM Provider

The GKVM provider reads content from a Git repository and exposes it as
Terraform data sources, starting with monitoring alert profiles.

It talks to the REST API of the repository platform. Both supported platforms
work in every deployment shape — public SaaS, enterprise tenant, or a server
your own organization runs — because the endpoint is configurable.

## Example Usage

### GitHub.com

```hcl
provider "gkvm" {
  repository = "glueckkanja/gkvm-monitoring-defaults"
  ref        = "main"
}
```

### GitHub Enterprise Cloud with data residency

The API lives on an `api.` hostname under your `ghe.com` subdomain.

```hcl
provider "gkvm" {
  base_url   = "https://api.SUBDOMAIN.ghe.com"
  repository = "my-org/gkvm-monitoring-defaults"
  ref        = "main"
}
```

### GitHub Enterprise Server

Point `base_url` at the instance; the `/api/v3` suffix is added for you.

```hcl
provider "gkvm" {
  base_url   = "https://github.example.com"
  repository = "my-org/gkvm-monitoring-defaults"
  ref        = "main"
}
```

### GitLab.com

```hcl
provider "gkvm" {
  platform   = "gitlab"
  repository = "my-group/gkvm-monitoring-defaults"
  ref        = "main"
}
```

### GitLab self-managed

Point `base_url` at the instance; the `/api/v4` suffix is added for you.
Projects may sit in nested subgroups.

```hcl
provider "gkvm" {
  platform   = "gitlab"
  base_url   = "https://gitlab.example.com"
  repository = "my-group/platform/gkvm-monitoring-defaults"
  ref        = "main"
}
```

## Endpoints

`base_url` takes the REST API endpoint. When it is omitted, the platform
default applies. Anything that already carries a URL path is used verbatim, so
an unusual deployment or an API gateway can be addressed exactly.

| Deployment | `platform` | `base_url` | Effective endpoint |
|---|---|---|---|
| GitHub.com | `github` | *(omit)* | `https://api.github.com` |
| GitHub Enterprise Cloud | `github` | *(omit)* | `https://api.github.com` |
| GitHub Enterprise Cloud, data residency | `github` | `https://api.SUBDOMAIN.ghe.com` | as given |
| GitHub Enterprise Server | `github` | `https://HOSTNAME` | `https://HOSTNAME/api/v3` |
| GitLab.com | `gitlab` | *(omit)* | `https://gitlab.com/api/v4` |
| GitLab self-managed / dedicated | `gitlab` | `https://HOSTNAME` | `https://HOSTNAME/api/v4` |

`base_url` must use `https`. A proxy is configured through the standard
`HTTPS_PROXY` / `NO_PROXY` environment variables, not by changing the scheme.

## Authentication

The token is resolved from the first source that yields a value:

| Order | GitHub | GitLab |
|---|---|---|
| 1 | `token` in the provider block | `token` in the provider block |
| 2 | `GKVM_TOKEN` | `GKVM_TOKEN` |
| 3 | `GH_TOKEN` | `GITLAB_TOKEN` |
| 4 | `GITHUB_TOKEN` | `CI_JOB_TOKEN` |
| 5 | `gh auth token --hostname HOST` | `glab auth token --hostname HOST` |

`GH_TOKEN` is checked before `GITHUB_TOKEN` because GitHub Actions injects
`GITHUB_TOKEN` into every step scoped to the calling repository only, whereas
`GH_TOKEN` is an explicit override.

The CLI is asked for the host that `base_url` resolves to, not for whatever
host the CLI happens to default to. Run `gh auth login --hostname HOST` (or
`glab auth login --hostname HOST`) once for the deployment you read from. The
`gh auth token` fallback needs the `gh` CLI on `PATH`; the `glab auth token`
fallback needs a `glab` version that provides that subcommand. A missing CLI is
not an error — the provider simply reports that no token was found.

Required permissions:

- **GitHub** — `contents: read` on the repository. A fine-grained personal
  access token, a GitHub App installation token, or a classic token with `repo`
  for a private repository all work.
- **GitLab** — a token with the `read_api` scope whose identity has at least
  the Reporter role on the project. Personal, group and project access tokens
  are sent as `PRIVATE-TOKEN`. A `CI_JOB_TOKEN` is sent as `JOB-TOKEN` instead,
  because GitLab rejects a job token presented as a private token; reading
  another project with a job token requires that project to allow it.

Tokens are never written to state and never appear in error messages.

## Network requirements

Every request goes to the one host in `base_url` — the directory listing and
the file contents alike. Raw file content is read through the API with the raw
media type rather than by following the `download_url` the API returns, so no
second hostname has to be reachable and the token is never sent anywhere else.

Allow outbound HTTPS from the machine running Terraform to:

| Platform | Host |
|---|---|
| GitHub.com | `api.github.com` |
| GitHub Enterprise | the host in `base_url` |
| GitLab | the host in `base_url` (`gitlab.com` by default) |

## Migrating from v0.1.x

The GitHub-only attribute names still work and now emit a deprecation warning.
Rename them when convenient:

| v0.1.x | Replacement |
|---|---|
| `github_repo` | `repository` |
| `github_ref` | `ref` |
| `github_token` | `token` |

Setting both spellings to different values is an error. A configuration that
only sets the old names keeps reading GitHub.com exactly as before.

## Schema

### Optional

- `platform` (String) Repository platform: `github` (default) or `gitlab`.
- `base_url` (String) REST API endpoint of the deployment. Defaults to
  `https://api.github.com` or `https://gitlab.com/api/v4` depending on
  `platform`, and falls back to the `GKVM_BASE_URL` environment variable. Must
  use `https`.
- `repository` (String) Repository to read from. GitHub: `owner/repo`. GitLab:
  project path with namespace, e.g. `group/project` or
  `group/subgroup/project`. Required in practice — it may be supplied through
  the deprecated `github_repo` instead.
- `ref` (String) Git ref to fetch (branch, tag, or commit SHA). Defaults to
  `main`.
- `token` (String, Sensitive) Access token with read access to the repository
  contents. Resolved from the environment or the platform CLI when unset.
- `github_repo` (String, **Deprecated**) Use `repository`.
- `github_ref` (String, **Deprecated**) Use `ref`.
- `github_token` (String, Sensitive, **Deprecated**) Use `token`.
