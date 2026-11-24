package provider

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/glueckkanja/terraform-provider-gkvm/internal/github"
	"github.com/glueckkanja/terraform-provider-gkvm/internal/gitlab"
	"github.com/glueckkanja/terraform-provider-gkvm/internal/local"
	"github.com/glueckkanja/terraform-provider-gkvm/internal/source"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Supported values of the platform attribute.
const (
	PlatformGitHub = "github"
	PlatformGitLab = "gitlab"
	PlatformLocal  = local.Platform
)

// Environment variables the provider reads, in the order it reads them.
// These constants hold the names of the variables, never a credential — hence
// the nosec annotations on the token-shaped names.
const (
	envBaseURL     = "GKVM_BASE_URL"
	envLocalDir    = "GKVM_LOCAL_DIR"
	envToken       = "GKVM_TOKEN"   //nolint:gosec // holds the name of an environment variable, not a credential
	envGHToken     = "GH_TOKEN"     //nolint:gosec // holds the name of an environment variable, not a credential
	envGitHubToken = "GITHUB_TOKEN" //nolint:gosec // holds the name of an environment variable, not a credential
	envGitLabToken = "GITLAB_TOKEN" //nolint:gosec // holds the name of an environment variable, not a credential
	envCIJobToken  = "CI_JOB_TOKEN" //nolint:gosec // holds the name of an environment variable, not a credential
)

var _ provider.Provider = &GkvmProvider{}

type GkvmProvider struct {
	version string
}

type gkvmProviderModel struct {
	Platform   types.String `tfsdk:"platform"`
	BaseURL    types.String `tfsdk:"base_url"`
	Repository types.String `tfsdk:"repository"`
	Ref        types.String `tfsdk:"ref"`
	Token      types.String `tfsdk:"token"`
	LocalDir   types.String `tfsdk:"local_dir"`

	// The GitHub-only spellings of v0.1.x, kept working and marked deprecated
	// in the schema.
	GithubRepo  types.String `tfsdk:"github_repo"`
	GithubRef   types.String `tfsdk:"github_ref"`
	GithubToken types.String `tfsdk:"github_token"`
}

// providerData is shared with data sources via Configure().
type providerData struct {
	Client source.Client
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &GkvmProvider{version: version}
	}
}

func (p *GkvmProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "gkvm"
	resp.Version = p.version
}

func (p *GkvmProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "GKVM platform provider — reads content from a Git repository for glueckkanja verified modules. Supports GitHub (including GitHub Enterprise Cloud and Enterprise Server), GitLab (including self-managed instances), and a directory on disk.",
		Attributes: map[string]schema.Attribute{
			"platform": schema.StringAttribute{
				Optional:    true,
				Description: "Repository platform to read from: \"github\" (default), \"gitlab\", or \"local\" to read " + envLocalDir + " / local_dir instead of an API.",
			},
			"base_url": schema.StringAttribute{
				Optional: true,
				Description: "REST API endpoint of the deployment to read from. Defaults to \"https://api.github.com\" or \"https://gitlab.com/api/v4\" depending on platform, " +
					"and falls back to the " + envBaseURL + " environment variable. " +
					"GitHub Enterprise Cloud with data residency: \"https://api.SUBDOMAIN.ghe.com\". GitHub Enterprise Server: \"https://HOSTNAME\" (the /api/v3 suffix is added). " +
					"GitLab self-managed: \"https://HOSTNAME\" (the /api/v4 suffix is added). Must use https.",
			},
			"repository": schema.StringAttribute{
				Optional:    true,
				Description: "Repository to read from. GitHub: \"owner/repo\". GitLab: project path with namespace, e.g. \"group/project\" or \"group/subgroup/project\".",
			},
			"ref": schema.StringAttribute{
				Optional:    true,
				Description: "Git ref to fetch (branch, tag, or commit SHA). Defaults to \"main\".",
			},
			"token": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				Description: "Access token with read access to the repository contents. Resolved from " + envToken + ", then platform-specific environment variables, then the platform CLI when unset. " +
					"GitHub: " + envGHToken + ", " + envGitHubToken + ", then 'gh auth token'. GitLab: " + envGitLabToken + ", then 'glab auth token'. " +
					"A GitLab CI job token cannot be used — GitLab's job token allowlist does not cover the repository tree endpoint needed to discover profiles.",
			},
			"local_dir": schema.StringAttribute{
				Optional: true,
				Description: "Absolute path of a directory holding the repository content, read instead of the API. Falls back to the " + envLocalDir + " environment variable, " +
					"and setting either one selects platform \"local\" unless platform says otherwise. Reads become local file I/O: no requests, no rate limit and no token. " +
					"Use it where one run reads the profiles many times over — a CI job that already has the content on disk serves every reader from it.",
			},

			"github_repo": schema.StringAttribute{
				Optional:           true,
				DeprecationMessage: "Use repository instead. github_repo will be removed in a future release.",
				Description:        "Deprecated: use repository.",
			},
			"github_ref": schema.StringAttribute{
				Optional:           true,
				DeprecationMessage: "Use ref instead. github_ref will be removed in a future release.",
				Description:        "Deprecated: use ref.",
			},
			"github_token": schema.StringAttribute{
				Optional:           true,
				Sensitive:          true,
				DeprecationMessage: "Use token instead. github_token will be removed in a future release.",
				Description:        "Deprecated: use token.",
			},
		},
	}
}

func (p *GkvmProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config gkvmProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if config.Platform.IsUnknown() {
		resp.Diagnostics.AddError(
			"Invalid provider configuration",
			"platform is not known at plan time. It decides whether the profiles are read from an API or from a directory, so it has to be a literal.",
		)
		return
	}
	platform := strings.ToLower(strings.TrimSpace(stringValue(config.Platform)))
	// Each candidate is trimmed before the fallback, not after: a blank-but-set
	// value would otherwise shadow a good one and then read as unset.
	localDir, localDirBlank := resolveLocalDir(config.LocalDir)

	// The environment selects the local backend on its own, so a CI job can
	// switch a generated configuration it cannot edit. An explicit platform wins.
	if platform == "" && localDir != "" {
		platform = PlatformLocal
	}
	if platform == "" {
		platform = PlatformGitHub
	}
	if platform != PlatformGitHub && platform != PlatformGitLab && platform != PlatformLocal {
		resp.Diagnostics.AddError(
			"Invalid provider configuration",
			fmt.Sprintf("platform %q is not supported — use %q, %q or %q.", platform, PlatformGitHub, PlatformGitLab, PlatformLocal),
		)
		return
	}

	// A set-but-blank value is the silent version of the failure this backend
	// exists to remove: every reader quietly goes back to the API.
	if localDirBlank {
		resp.Diagnostics.AddError(
			"Invalid provider configuration",
			fmt.Sprintf("%s or local_dir is set but holds only whitespace. Unset it to read the API, or give it the directory to read.", envLocalDir),
		)
		return
	}

	// The mirror of the warning the local platform emits: an operator who
	// exports GKVM_LOCAL_DIR to escape a rate limit must not be left guessing
	// why one root still reads the API.
	if platform != PlatformLocal && localDir != "" {
		resp.Diagnostics.AddWarning(
			"Local profile directory ignored",
			fmt.Sprintf("A local profile directory is set but platform is %q, so the profiles are read from the API. Remove the platform attribute to read the directory instead.", platform),
		)
	}

	// A left-over github_* attribute on another API platform is not a harmless
	// alias: github_token would hand a GitHub credential to that host. Only the
	// local platform is exempt — no host, no credential — so a platform added
	// later is guarded without anyone remembering to add it here.
	if platform != PlatformGitHub && platform != PlatformLocal {
		for name, value := range map[string]types.String{
			"github_repo":  config.GithubRepo,
			"github_ref":   config.GithubRef,
			"github_token": config.GithubToken,
		} {
			if stringValue(value) != "" {
				resp.Diagnostics.AddError(
					"Invalid provider configuration",
					fmt.Sprintf("%s is set while platform is %q. The github_* attributes are the deprecated GitHub-only spellings; rename them to the platform-neutral repository, ref and token before switching platform.", name, platform),
				)
				return
			}
		}
	}

	repository, err := coalesce("repository", config.Repository, "github_repo", config.GithubRepo)
	if err != nil {
		resp.Diagnostics.AddError("Invalid provider configuration", err.Error())
		return
	}
	// The local backend reads a directory; a repository is optional there and
	// only names what the directory should hold.
	if repository == "" && platform != PlatformLocal {
		resp.Diagnostics.AddError(
			"Invalid provider configuration",
			"repository must be set — the repository to read profiles from, e.g. \"owner/repo\" on GitHub or \"group/project\" on GitLab.",
		)
		return
	}

	ref, err := coalesce("ref", config.Ref, "github_ref", config.GithubRef)
	if err != nil {
		resp.Diagnostics.AddError("Invalid provider configuration", err.Error())
		return
	}
	if ref == "" {
		ref = "main"
	}

	configuredToken, err := coalesce("token", config.Token, "github_token", config.GithubToken)
	if err != nil {
		resp.Diagnostics.AddError("Invalid provider configuration", err.Error())
		return
	}

	// The endpoint is resolved before the token: which credential store the
	// platform CLI is asked for depends on the host being addressed.
	baseURL := firstNonEmpty(stringValue(config.BaseURL), os.Getenv(envBaseURL))
	// Not validated for the local platform: it addresses no endpoint, and a
	// malformed one inherited from the environment must not stop a read that
	// never leaves the machine.
	if baseURL != "" && platform != PlatformLocal {
		if err := source.ValidateBaseURL(baseURL); err != nil {
			resp.Diagnostics.AddError("Invalid provider configuration", err.Error())
			return
		}
	}

	var (
		client   source.Client
		endpoint string
	)

	switch platform {
	case PlatformLocal:
		// Nothing proves the directory holds what this configuration asked
		// for. When the environment made that choice rather than the
		// configuration, say so: the repository named here is not being read.
		if stringValue(config.LocalDir) == "" && (stringValue(config.Repository) != "" || stringValue(config.GithubRepo) != "") {
			resp.Diagnostics.AddWarning(
				"Profiles read from a directory, not the configured repository",
				fmt.Sprintf("%s selected a local profile directory, so %q at %q is not read and nothing verifies that the directory holds it. Unset the variable to read the repository.",
					envLocalDir, repository, ref),
			)
		}
		// No token is resolved: nothing here reaches the network, and the
		// platform CLI would otherwise be run once per reader for nothing.
		warnIgnoredByLocal(resp, map[string]string{
			"base_url":     stringValue(config.BaseURL),
			"token":        stringValue(config.Token),
			"github_token": stringValue(config.GithubToken),
		})
		client = &local.Client{
			Dir:        localDir,
			Repository: repository,
			Ref:        ref,
		}
	case PlatformGitLab:
		endpoint = gitlab.NormalizeBaseURL(baseURL)
		token := resolveGitLabToken(ctx, configuredToken, endpoint)
		if token == "" {
			resp.Diagnostics.AddError("GitLab token not found", gitlabTokenHelp(endpoint))
			return
		}
		client = &gitlab.Client{
			Token:   token,
			Project: repository,
			Ref:     ref,
			BaseURL: endpoint,
		}
	default:
		endpoint = github.NormalizeBaseURL(baseURL)
		token := resolveGitHubToken(ctx, configuredToken, endpoint)
		if token == "" {
			resp.Diagnostics.AddError("GitHub token not found", githubTokenHelp(endpoint))
			return
		}
		client = &github.Client{
			Token:   token,
			Repo:    repository,
			Ref:     ref,
			BaseURL: endpoint,
		}
	}

	if err := client.ValidateConfig(); err != nil {
		resp.Diagnostics.AddError("Invalid provider configuration", err.Error())
		return
	}

	// A small budget, not the read's: the probe runs once per process, so a
	// long wait multiplies by the unit count, while a concurrency burst from
	// those same processes clears in a moment.
	if err := client.Ping(source.WithRateLimitBudget(ctx, source.ProbeWaitBudget)); err != nil {
		title := "Failed to connect to the repository"
		if platform == PlatformLocal {
			title = "Failed to read the local profile directory"
		}
		resp.Diagnostics.AddError(
			title,
			fmt.Sprintf("Platform: %s\nEndpoint: %s\nRepository: %s\nRef: %s\nError: %s",
				platform, client.Endpoint(), client.Subject(), client.Reference(), err.Error()),
		)
		return
	}

	resp.DataSourceData = &providerData{Client: client}
}

// resolveLocalDir reads the local directory from the configuration, then the
// environment, trimming each candidate. It reports separately that a candidate
// was present but blank, which must not pass as "not configured".
func resolveLocalDir(configured types.String) (dir string, blank bool) {
	fromConfig := stringValue(configured)
	fromEnv, envSet := os.LookupEnv(envLocalDir)

	for _, candidate := range []struct {
		value string
		set   bool
	}{
		{fromConfig, fromConfig != ""},
		{fromEnv, envSet},
	} {
		if trimmed := strings.TrimSpace(candidate.value); trimmed != "" {
			return trimmed, false
		}
		if candidate.set {
			blank = true
		}
	}
	return "", blank
}

// warnIgnoredByLocal reports attributes the local backend has no use for. It
// warns rather than fails: the configuration it reads was written for the API.
func warnIgnoredByLocal(resp *provider.ConfigureResponse, values map[string]string) {
	ignored := make([]string, 0, len(values))
	for name, value := range values {
		if value != "" {
			ignored = append(ignored, name)
		}
	}
	if len(ignored) == 0 {
		return
	}
	sort.Strings(ignored)
	resp.Diagnostics.AddWarning(
		"Attributes ignored by the local platform",
		fmt.Sprintf("%s %s set but the local platform reads a directory: no request is made and no token is sent. Remove them, or set platform explicitly to read the API instead.",
			strings.Join(ignored, ", "), plural(len(ignored))),
	)
}

// plural picks the verb for a list of n names.
func plural(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// resolveGitHubToken returns the first token it can find, in order:
// provider configuration, GKVM_TOKEN, GH_TOKEN, GITHUB_TOKEN, gh CLI.
//
// GH_TOKEN is checked before GITHUB_TOKEN because GITHUB_TOKEN is automatically
// injected by GitHub Actions for every step (scoped to the calling repo only),
// whereas GH_TOKEN is an explicit user override — it should take precedence.
func resolveGitHubToken(ctx context.Context, configured, endpoint string) string {
	if token := firstNonEmpty(configured, os.Getenv(envToken), os.Getenv(envGHToken), os.Getenv(envGitHubToken)); token != "" {
		return token
	}
	// The CLI stores one credential per deployment, so it is asked for the
	// host being addressed rather than for whatever host it defaults to.
	return cliToken(ctx, "gh", github.CLIHost(endpoint))
}

// resolveGitLabToken returns the first token it can find, in order: provider
// configuration, GKVM_TOKEN, GITLAB_TOKEN, glab CLI.
//
// CI_JOB_TOKEN is deliberately not in the chain. GitLab's job token allowlist
// covers GET /projects/:id/repository/files/:file_path/raw but not the
// repository tree endpoint this provider needs to discover profiles, and on a
// route outside the allowlist GitLab ignores the JOB-TOKEN header instead of
// rejecting it. A job token would therefore read as anonymous and fail with a
// 404 that looks like a wrong project. gitlabTokenHelp names it instead.
func resolveGitLabToken(ctx context.Context, configured, endpoint string) string {
	if t := firstNonEmpty(configured, os.Getenv(envToken), os.Getenv(envGitLabToken)); t != "" {
		return t
	}
	return cliToken(ctx, "glab", gitlab.CLIHost(endpoint))
}

// cliToken asks a platform CLI for the stored token of one host. A missing CLI,
// a CLI without that subcommand, or a host it holds no credential for all mean
// "no token" — the caller reports that with actionable help.
func cliToken(ctx context.Context, binary, host string) string {
	args := []string{"auth", "token"}
	if host != "" {
		args = append(args, "--hostname", host)
	}
	// The binary is a literal from this file's two call sites and the host has
	// been validated as the host of an https URL. Arguments are passed as argv
	// elements to that binary, never through a shell, so a hostile value cannot
	// become a second command.
	out, err := exec.CommandContext(ctx, binary, args...).Output() //nolint:gosec // fixed binary, argv passing, no shell
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func githubTokenHelp(endpoint string) string {
	return fmt.Sprintf(
		"No token found for %s.\nSet token in the provider configuration, set %s / %s / %s, or run 'gh auth login --hostname %s'.\nThe token needs 'contents: read' on the repository.",
		endpoint, envToken, envGHToken, envGitHubToken, github.CLIHost(endpoint))
}

func gitlabTokenHelp(endpoint string) string {
	help := fmt.Sprintf(
		"No token found for %s.\nSet token in the provider configuration, set %s / %s, or run 'glab auth login --hostname %s'.\nThe token needs the read_api scope (or read_repository) on the project.",
		endpoint, envToken, envGitLabToken, gitlab.CLIHost(endpoint))

	// A pipeline is the likeliest place to hit this, and the job token sitting
	// right there is the likeliest thing to reach for, so say why it cannot work.
	if os.Getenv(envCIJobToken) != "" {
		help += fmt.Sprintf(
			"\n\nA CI job token is present in %s but cannot be used: GitLab's job token allowlist covers the raw file endpoint but not the repository tree endpoint this provider needs to discover the profiles, and GitLab ignores the JOB-TOKEN header on routes outside that allowlist rather than rejecting it — the read would silently fall back to anonymous access. Use a project or group access token with read_api instead.",
			envCIJobToken)
	}
	return help
}

// coalesce returns the value of the current attribute, falling back to its
// deprecated spelling, and reports a conflict when both carry a value.
func coalesce(name string, current types.String, deprecatedName string, deprecated types.String) (string, error) {
	cur, dep := stringValue(current), stringValue(deprecated)
	if cur != "" && dep != "" && cur != dep {
		return "", fmt.Errorf("%s and %s are both set to different values — keep %s and remove the deprecated %s", name, deprecatedName, name, deprecatedName)
	}
	return firstNonEmpty(cur, dep), nil
}

func stringValue(v types.String) string {
	if v.IsNull() || v.IsUnknown() {
		return ""
	}
	return v.ValueString()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (p *GkvmProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewMonitoringProfilesDataSource,
	}
}

func (p *GkvmProvider) Resources(_ context.Context) []func() resource.Resource {
	return nil
}
