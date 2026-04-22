# Contributing

## Requirements

- Go >= 1.23
- `gh` CLI (for local dev token resolution)
- `golangci-lint` v2+ (for linting)

## Setup

```shell
git clone https://github.com/glueckkanja/terraform-provider-gkvm
cd terraform-provider-gkvm
go mod download
```

To use a local build with Terraform/OpenTofu, add a dev override to `~/.terraformrc`:

```hcl
provider_installation {
  dev_overrides {
    "glueckkanja/gkvm" = "/path/to/your/GOPATH/bin"
  }
  direct {}
}
```

Then install locally:

```shell
go install .
```

## Common commands

```shell
make build      # compile
make test       # run unit tests with race detector
make lint       # run golangci-lint
make cover      # run tests and open HTML coverage report
```

## Pull Requests

- One concern per PR
- All tests must pass (`make test`)
- Lint must be clean (`make lint`)
- Update `CHANGELOG.md` under an `## [Unreleased]` heading
- PRs require one approving review before merge

## Tests

Unit tests live alongside source files (`*_test.go`). Run them with:

```shell
go test -race ./...
```

Acceptance tests require a real GitHub token and a target repository. Set `TF_ACC=1` and `GITHUB_TOKEN` before running:

```shell
TF_ACC=1 GITHUB_TOKEN=<token> go test -v ./internal/provider/...
```

## Releasing

Releases are triggered by pushing a `v*` tag. GoReleaser builds and signs the binaries automatically via CI. See `.goreleaser.yml` for the full pipeline.
