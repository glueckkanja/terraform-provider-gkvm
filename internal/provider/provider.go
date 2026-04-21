package provider

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/glueckkanja/terraform-provider-gkvm/internal/github"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ provider.Provider = &GkvmProvider{}

type GkvmProvider struct {
	version string
}

type gkvmProviderModel struct {
	GithubRepo  types.String `tfsdk:"github_repo"`
	GithubRef   types.String `tfsdk:"github_ref"`
	GithubToken types.String `tfsdk:"github_token"`
}

// providerData is shared with data sources via Configure().
type providerData struct {
	GitHubClient *github.Client
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
		Description: "GKVM platform provider — reads content from a GitHub repository for glueckkanja verified modules.",
		Attributes: map[string]schema.Attribute{
			"github_repo": schema.StringAttribute{
				Required:    true,
				Description: "GitHub repository to read from (e.g., \"glueckkanja/gkvm-monitoring-defaults\").",
			},
			"github_ref": schema.StringAttribute{
				Optional:    true,
				Description: "Git ref to fetch (branch, tag, or commit SHA). Defaults to \"main\".",
			},
			"github_token": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "GitHub personal access token. Falls back to GITHUB_TOKEN / GH_TOKEN env vars, then 'gh auth token'. Only set explicitly if not using the gh CLI.",
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

	// Resolve GitHub token: config > GITHUB_TOKEN > GH_TOKEN > gh CLI
	token := ""
	if !config.GithubToken.IsNull() && !config.GithubToken.IsUnknown() {
		token = config.GithubToken.ValueString()
	}
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	if token == "" {
		if out, err := exec.Command("gh", "auth", "token").Output(); err == nil {
			token = strings.TrimSpace(string(out))
		}
	}
	if token == "" {
		resp.Diagnostics.AddError(
			"GitHub token not found",
			"Run 'gh auth login' once, or set GITHUB_TOKEN / GH_TOKEN, or set github_token in provider config.",
		)
		return
	}

	ref := "main"
	if !config.GithubRef.IsNull() && !config.GithubRef.IsUnknown() {
		ref = config.GithubRef.ValueString()
	}

	client := &github.Client{
		Token: token,
		Repo:  config.GithubRepo.ValueString(),
		Ref:   ref,
	}

	if err := client.ValidateConfig(); err != nil {
		resp.Diagnostics.AddError("Invalid provider configuration", err.Error())
		return
	}

	// Validate connectivity with a lightweight repository check (fail-fast).
	if err := client.Ping(); err != nil {
		resp.Diagnostics.AddError(
			"Failed to connect to GitHub repository",
			fmt.Sprintf("Repository: %s, Ref: %s\nError: %s", client.Repo, client.Ref, err.Error()),
		)
		return
	}

	resp.DataSourceData = &providerData{GitHubClient: client}
}

func (p *GkvmProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewMonitoringProfilesDataSource,
	}
}

func (p *GkvmProvider) Resources(_ context.Context) []func() resource.Resource {
	return nil
}
