# Changelog

## [Unreleased] — Configurable endpoint and GitLab support

### Overview

The provider is no longer bound to GitHub.com. The API endpoint is
configurable, which makes GitHub Enterprise Server and GitHub Enterprise Cloud
with data residency reachable, and GitLab is supported as a second platform.

Existing configurations keep working unchanged; the GitHub-only attribute
names are deprecated, not removed.

### Added

- `base_url` — REST API endpoint of the deployment to read from. Defaults to
  `https://api.github.com` or `https://gitlab.com/api/v4` depending on
  `platform`, and falls back to the `GKVM_BASE_URL` environment variable.
  A bare hostname is completed with the platform's API path (`/api/v3` for
  GitHub Enterprise Server, `/api/v4` for GitLab), while an endpoint that
  already carries a path is used verbatim. A `ghe.com` host is the one
  exception and is always normalized to `https://api.SUBDOMAIN.ghe.com`:
  data residency tenants serve the API only from that form, and the host a
  user has in hand is the web one they log in to. HTTPS is required.
- `platform` — `github` (default) or `gitlab`.
- GitLab backend: project paths with nested subgroups, tree listings paged
  explicitly at 100 entries (the endpoint defaults to 20, which would silently
  truncate a profile directory), and raw file reads through the Repository
  Files API.
- Platform-neutral `repository`, `ref` and `token` attributes.
- The caller's context reaches the HTTP requests, so a cancelled plan stops the
  provider instead of running out the per-request timeout for every file.
- `GKVM_TOKEN` environment variable, read by both platforms.
- GitLab token resolution: `GITLAB_TOKEN`, then `CI_JOB_TOKEN`, then
  `glab auth token`. A CI job token is sent as `JOB-TOKEN`, which is the only
  header GitLab accepts it in.

### Changed

- Raw file content is read through the Contents API with the raw media type
  instead of following the `download_url` from the directory listing. Every
  request now stays on the configured endpoint: no `raw.githubusercontent.com`
  (or an Enterprise deployment's equivalent) has to be reachable, and the token
  is never sent to a second host.
- The platform CLI fallback asks for the token of the host that `base_url`
  resolves to (`gh auth token --hostname HOST`). Previously the CLI default
  host was used, so a CLI logged into an Enterprise host handed out a token
  that `api.github.com` then rejected with a misleading 401.
- Error messages name the endpoint, the repository and the ref, and a failed
  connection reports the transport cause ("no such host", "connection
  refused", a TLS error) instead of a generic "connection error". A wrong
  endpoint is the likeliest misconfiguration and the old message sent the user
  looking at their token. The request URL is still kept out of the message.
- The GitLab raw file request asks for `*/*` instead of `application/json`.
  The endpoint answers with file bytes of any type, so a gateway in front of a
  self-managed instance could have answered 406.

### Security

- Redirects are pinned to the configured endpoint. `net/http` follows
  redirects by default and drops only `Authorization`, `Www-Authenticate`,
  `Cookie` and `Cookie2` when the target is another domain. GitLab
  authenticates with `PRIVATE-TOKEN` / `JOB-TOKEN`, which are not on that list,
  and a redirect to a different port of the same host keeps every header
  including `Authorization`. A redirect that leaves the configured endpoint is
  now refused with an error that says so; one that stays on it is still
  followed, so a renamed repository keeps working.
- A response larger than the 10 MB cap is now an error instead of a truncated
  body. A YAML profile cut at the cap is frequently still valid YAML, so the
  alert rules past the cut would have been dropped without any error.
- Setting a `github_*` attribute while `platform` is not `github` is now an
  error. Previously a `github_token` left behind during a migration would have
  been sent to the GitLab host as a private token.

### Fixed

- `CLIHost` no longer strips a leading `api.` from a path-based endpoint, so an
  Enterprise Server named `api.something` is asked for under its own hostname,
  and no longer carries a port into `--hostname`, which neither CLI accepts.
- A GitHub Enterprise Server whose hostname begins with `api.` now receives the
  `/api/v3` path. Only `api.github.com` publishes the API on a bare `api.` host.
- A GitHub directory listing that comes back at the Contents API's 1000-entry
  cap is reported as possibly incomplete instead of being treated as the whole
  directory.
- Repository paths are validated segment by segment and reject relative path
  elements. `..` consists of allowed characters, so the previous pattern
  accepted a repository such as `../other-repo`.
- Repository and file paths are percent-encoded when built into request URLs.

### Deprecated

| Attribute | Replacement |
|-----------|-------------|
| `github_repo` | `repository` |
| `github_ref` | `ref` |
| `github_token` | `token` |

The old names still configure the provider and emit a deprecation warning.
Setting both spellings of one value to different values is an error.

---

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
