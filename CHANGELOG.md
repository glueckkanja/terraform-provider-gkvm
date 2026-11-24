# Changelog

## [0.3.0] — Read profiles from a directory

### Added

- `platform = "local"` and `local_dir` — read the profiles from a directory on
  disk instead of from a repository API. Reads become local file I/O: no
  requests, no rate limit, and no token (none is resolved, and the platform
  CLI is not run either).

  One provider instance reads the profiles with one directory listing plus one
  request per profile file, which is fine for a single configuration and
  expensive for a tool that runs many: every OpenTofu process holds its own
  provider instance, so a run over N units repeats that work N times against
  the same unchanging content. With a few dozen profiles and a few dozen units
  it is the rate limit, not the content, that decides whether the run finishes.

  `local_dir` also comes from the `GKVM_LOCAL_DIR` environment variable, and
  setting either one selects the local platform unless `platform` says
  otherwise. That is what makes the backend reachable for a generated
  configuration nobody can edit: a CI job that already clones the repository
  exports the variable, and every reader in the run follows it. An explicit
  `platform` always wins, so a configuration can insist on reading the API.

  The directory stands in for the repository root, so `profile_path` keeps its
  meaning. `repository` and `ref` may still be set and are reported in error
  messages, but nothing verifies them against the directory — a directory
  carries no provenance, so pin the ref in whatever fetches it. `base_url`,
  `token` and `github_token` are ignored and reported as a warning rather than
  an error, because the configuration this backend is reached from was written
  for the API.

  Failures are loud: a directory that is missing, is not a directory, or cannot
  be listed is reported when the provider is configured, and one that holds no
  YAML profile is reported when it is read. A silently empty profile set would
  become a plan that drops every default alert, and a missing alert is not
  something a plan shows.

### Fixed

- A rate-limited HTTP 403 is retried and named as a rate limit. GitHub answers
  both "this token may not read that" and "you are over a secondary rate
  limit" with 403, and only the first was ever treated as permanent — so a
  secondary limit failed the read on the first attempt and blamed the token
  while doing it (`check token permissions`). A 403 that carries a
  `Retry-After` header or an exhausted remaining-request counter is now
  retried like a 429, and its message names the limit instead of the token. A
  403 without that evidence stays permanent and keeps its wording, so a
  genuinely insufficient token still fails on the first attempt.

- A rate limit that outlasts the read is reported instead of retried. A limit
  asking for half an hour cannot be waited out in five attempts, so the read
  now says how long it lasts rather than spending its attempts to fail
  anyway.

  The budget is the read's, not each request's: one profile read makes a
  listing plus one request per file, and each of those waiting out the bound
  separately would turn a throttled plan into minutes of sleeping. 90 seconds
  covers a whole read — more than the 10 second cap applied to a 5xx, because
  a limit lifts only when its window moves while a server error may be gone on
  the next try. A request outside a read gets its own budget of the same size.

- A non-regular file in a local profile directory is refused by name instead of
  blocking the read. The kind is checked before the file is opened, because
  opening a FIFO blocks until a writer appears and a check on the open handle
  never runs.

- A path in a local profile directory that cannot be resolved for any reason
  other than non-existence is reported. Treating every resolution failure as
  "not there" skipped the containment check that follows it.

- `local_dir` or `GKVM_LOCAL_DIR` set while `platform` names an API platform
  now warns that the directory is ignored, the mirror of the warning the local
  platform already emitted for `base_url` and `token`. Silently reading the API
  is the failure mode this backend exists to remove.

- `local_dir` and `GKVM_LOCAL_DIR` are trimmed **per candidate**, so a blank
  attribute no longer shadows a usable variable, and a value holding only
  whitespace is an error rather than a silent return to the API. A workflow
  writing `GKVM_LOCAL_DIR=` from a skipped step used to send every reader back
  to the REST API with no diagnostic at all.

- A `platform` that is unknown at plan time is an error. It decided whether the
  profiles come from an API or a directory, and an unknown value read as unset —
  which let the environment override a configuration that insists on the API.

- A rate-limit counter of exactly zero counts as exhausted. Some proxies send
  `-1` for "no limit", and treating that as exhausted reclassified every
  permission 403 as throttling — the mis-blame in reverse.

- `ListDirectory` resolves an entry's kind through a symlink, so a link to a
  directory is reported as one. Reported as a file it was handed to the file
  read, which refused it and failed the whole profile read.

- The local backend observes a cancelled context between reads. A blocked
  syscall on a stale mount still cannot be interrupted, but a cancelled plan no
  longer reads the rest of the directory first.

- The reachability check's budget is 15 s rather than 5 s. Five seconds is
  below the plain backoff ladder of 0.5+1+2+4 = 7.5 s, so a throttled probe lost
  an attempt it used to get, and a `Retry-After` between 5 s and 10 s failed
  where the previous release waited and recovered.

- `local_dir` and `GKVM_LOCAL_DIR` are trimmed. A trailing newline — what
  `echo "X=$dir" >> $GITHUB_ENV` produces — selected the local platform and
  then failed on a character invisible in the diagnostic.

- The reachability check made when the provider is configured gets a five
  second waiting budget rather than the read's ninety. It runs once per
  provider instance, so a long wait multiplies by the number of readers, while
  the concurrency burst those same readers cause clears in a moment — failing
  on that would be the re-run-required failure retrying was added to remove.

- A throttling answer that asks for no wait at all is retried immediately
  instead of being refused for want of budget: refusing it spends nothing and
  fails anyway.

- A rate-limit reset timestamp already in the past is backed off rather than
  retried at once. A skewed clock or a stale proxy header would otherwise fire
  every attempt within milliseconds at an active limiter.

- `GKVM_LOCAL_DIR` selecting the directory while a `repository` is configured
  warns that the repository is not what is being read. Nothing can verify a
  directory against the repository it claims to hold, so the one case where the
  configuration did not ask for it is called out.

- `base_url` is no longer validated under `platform = "local"`. A malformed
  endpoint inherited from the environment stopped a read that never leaves the
  machine.

- The warning about attributes the local platform ignores names only the
  attributes that were set, and only those from the configuration — an endpoint
  resolved from `GKVM_BASE_URL` is not something the reader can remove. `token` and `github_token` are one value after
  coalescing, so a configuration setting just the deprecated spelling was told
  to remove an attribute it never had.

## [0.2.1] — Retry transient API failures

### Fixed

- Transient API failures are retried instead of failing the read. A single
  HTTP 5xx or 429 from the configured endpoint used to abort the data source,
  and because every consumer reads the profiles, one blip anywhere turned a
  whole plan or apply red with `Failed to fetch monitoring profiles`. Reads now
  make up to five attempts with an exponential backoff (500 ms doubling to a
  4 s cap), honour a bounded `Retry-After` on 429 and 503, and stop as soon as
  the context ends.

  Permanent answers are still reported on the first attempt: 401, 403, 404 and
  any other 4xx are never retried, nor is a redirect that leaves the configured
  endpoint, an oversized response, or a request that was refused up front. The
  error messages are unchanged, so an exhausted retry reports the last status
  exactly as before. The retry lives in the shared requester, so it applies to
  both the GitHub and GitLab backends.

## [0.2.0] — Configurable endpoint and GitLab support

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
- GitLab token resolution: `GITLAB_TOKEN`, then `glab auth token`. Tokens are
  sent as `PRIVATE-TOKEN`, which a live check confirms GitLab validates (an
  invalid one answers 401 even on a public project).
  A CI job token is deliberately **not** accepted: GitLab's job token allowlist
  covers `GET /projects/:id/repository/files/:file_path/raw` but not the
  repository tree endpoint needed to discover profiles, and GitLab ignores the
  `JOB-TOKEN` header on routes outside the allowlist rather than rejecting it
  (confirmed live: an invalid `JOB-TOKEN` still answers 200). A job token would
  therefore read as anonymous and fail with a 404 that looks like a wrong
  project. When `CI_JOB_TOKEN` is set and no usable token is found, the
  provider says exactly this instead of trying.

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
