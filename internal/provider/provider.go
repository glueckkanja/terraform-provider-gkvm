package provider

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/glueckkanja/terraform-provider-gkvm/internal/github"
	"github.com/glueckkanja/terraform-provider-gkvm/internal/gitlab"
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
)

// Environment variables the provider reads, in the order it reads them.
// These constants hold the names of the variables, never a credential — hence
// the nosec annotations on the token-shaped names.
const (
	envBaseURL     = "GKVM_BASE_URL"
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
		Description: "GKVM platform provider — reads content from a Git repository for glueckkanja verified modules. Supports GitHub (including GitHub Enterprise Cloud and Enterprise Server) and GitLab (including self-managed instances).",
		Attributes: map[string]schema.Attribute{
			"platform": schema.StringAttribute{
				Optional:    true,
				Description: "Repository platform to read from: \"github\" (default) or \"gitlab\".",
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
					"GitHub: " + envGHToken + ", " + envGitHubToken + ", then 'gh auth token'. GitLab: " + envGitLabToken + ", " + envCIJobToken + ", then 'glab auth token'.",
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

	platform := strings.ToLower(strings.TrimSpace(stringValue(config.Platform)))
	if platform == "" {
		platform = PlatformGitHub
	}
	if platform != PlatformGitHub && platform != PlatformGitLab {
		resp.Diagnostics.AddError(
			"Invalid provider configuration",
			fmt.Sprintf("platform %q is not supported — use %q or %q.", platform, PlatformGitHub, PlatformGitLab),
		)
		return
	}

	repository, err := coalesce("repository", config.Repository, "github_repo", config.GithubRepo)
	if err != nil {
		resp.Diagnostics.AddError("Invalid provider configuration", err.Error())
		return
	}
	if repository == "" {
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
	if baseURL != "" {
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
	case PlatformGitLab:
		endpoint = gitlab.NormalizeBaseURL(baseURL)
		token, header := resolveGitLabToken(ctx, configuredToken, endpoint)
		if token == "" {
			resp.Diagnostics.AddError("GitLab token not found", gitlabTokenHelp(endpoint))
			return
		}
		client = &gitlab.Client{
			Token:       token,
			TokenHeader: header,
			Project:     repository,
			Ref:         ref,
			BaseURL:     endpoint,
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

	if err := validateClient(client); err != nil {
		resp.Diagnostics.AddError("Invalid provider configuration", err.Error())
		return
	}

	// Validate connectivity with a lightweight repository check (fail-fast).
	if err := client.Ping(); err != nil {
		resp.Diagnostics.AddError(
			"Failed to connect to the repository",
			fmt.Sprintf("Platform: %s\nEndpoint: %s\nRepository: %s\nRef: %s\nError: %s",
				platform, client.Endpoint(), client.Subject(), client.Reference(), err.Error()),
		)
		return
	}

	resp.DataSourceData = &providerData{Client: client}
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

// resolveGitLabToken returns the first token it can find together with the
// header it has to be sent in, in order: provider configuration, GKVM_TOKEN,
// GITLAB_TOKEN, CI_JOB_TOKEN, glab CLI.
//
// A CI job token is rejected by GitLab when sent as a private token, so it is
// the one case that changes the header.
func resolveGitLabToken(ctx context.Context, configured, endpoint string) (token, header string) {
	if t := firstNonEmpty(configured, os.Getenv(envToken), os.Getenv(envGitLabToken)); t != "" {
		return t, gitlab.PrivateTokenHeader
	}
	if t := os.Getenv(envCIJobToken); t != "" {
		return t, gitlab.JobTokenHeader
	}
	return cliToken(ctx, "glab", gitlab.CLIHost(endpoint)), gitlab.PrivateTokenHeader
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
	return fmt.Sprintf(
		"No token found for %s.\nSet token in the provider configuration, set %s / %s / %s, or run 'glab auth login --hostname %s'.\nThe token needs the read_api scope (or read_repository) on the project.",
		endpoint, envToken, envGitLabToken, envCIJobToken, gitlab.CLIHost(endpoint))
}

// validateClient runs the backend-specific configuration checks.
func validateClient(client source.Client) error {
	type validator interface{ ValidateConfig() error }
	if v, ok := client.(validator); ok {
		return v.ValidateConfig()
	}
	return nil
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
