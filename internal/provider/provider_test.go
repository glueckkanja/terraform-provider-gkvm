package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestGkvmProvider_Metadata(t *testing.T) {
	p := &GkvmProvider{version: "1.2.3"}
	resp := &provider.MetadataResponse{}
	p.Metadata(context.Background(), provider.MetadataRequest{}, resp)

	if resp.TypeName != "gkvm" {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, "gkvm")
	}
	if resp.Version != "1.2.3" {
		t.Errorf("Version = %q, want %q", resp.Version, "1.2.3")
	}
}

func providerSchema(t *testing.T) schema.Schema {
	t.Helper()
	p := &GkvmProvider{}
	resp := &provider.SchemaResponse{}
	p.Schema(context.Background(), provider.SchemaRequest{}, resp)
	return resp.Schema
}

func TestGkvmProvider_Schema(t *testing.T) {
	attrs := providerSchema(t).Attributes

	for _, name := range []string{"platform", "base_url", "repository", "ref", "token"} {
		if _, ok := attrs[name]; !ok {
			t.Errorf("schema missing %s attribute", name)
		}
	}

	// The v0.1.x spellings stay accepted so existing configurations keep
	// working, but every one of them must announce its replacement.
	for _, name := range []string{"github_repo", "github_ref", "github_token"} {
		attr, ok := attrs[name]
		if !ok {
			t.Errorf("schema dropped the deprecated %s attribute", name)
			continue
		}
		if attr.GetDeprecationMessage() == "" {
			t.Errorf("%s is not marked deprecated", name)
		}
	}

	if !attrs["token"].IsSensitive() {
		t.Error("token is not marked sensitive")
	}
	if attrs["repository"].IsRequired() {
		t.Error("repository must stay optional so github_repo alone still configures the provider")
	}
}

func TestGkvmProvider_DataSources(t *testing.T) {
	if sources := (&GkvmProvider{}).DataSources(context.Background()); len(sources) == 0 {
		t.Error("expected at least one data source, got none")
	}
}

func TestGkvmProvider_Resources(t *testing.T) {
	if resources := (&GkvmProvider{}).Resources(context.Background()); resources != nil {
		t.Errorf("expected nil resources, got %v", resources)
	}
}

func TestNew_ReturnsProvider(t *testing.T) {
	if p := New("0.1.0")(); p == nil {
		t.Fatal("New() returned nil")
	}
}

// configure runs Configure against a literal provider block. GKVM_TOKEN is set
// so no platform CLI is invoked; every case below fails before any request is
// made, so none of them touches the network.
func configure(t *testing.T, attrs map[string]string) *provider.ConfigureResponse {
	t.Helper()
	t.Setenv(envToken, "dummy-token")
	t.Setenv(envBaseURL, "")

	s := providerSchema(t)
	objType, ok := s.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("provider schema is not an object type")
	}

	values := make(map[string]tftypes.Value, len(objType.AttributeTypes))
	for name := range objType.AttributeTypes {
		if v, set := attrs[name]; set {
			values[name] = tftypes.NewValue(tftypes.String, v)
			continue
		}
		values[name] = tftypes.NewValue(tftypes.String, nil)
	}

	resp := &provider.ConfigureResponse{}
	(&GkvmProvider{}).Configure(context.Background(),
		provider.ConfigureRequest{Config: tfsdk.Config{Schema: s, Raw: tftypes.NewValue(objType, values)}},
		resp)
	return resp
}

func TestConfigure_Rejects(t *testing.T) {
	tests := []struct {
		name       string
		attrs      map[string]string
		wantSubstr string
	}{
		{
			name:       "unknown platform",
			attrs:      map[string]string{"platform": "bitbucket", "repository": "owner/repo"},
			wantSubstr: "platform",
		},
		{
			name:       "no repository at all",
			attrs:      map[string]string{},
			wantSubstr: "repository must be set",
		},
		{
			name:       "repository and github_repo disagree",
			attrs:      map[string]string{"repository": "owner/repo", "github_repo": "other/repo"},
			wantSubstr: "github_repo",
		},
		{
			name:       "ref and github_ref disagree",
			attrs:      map[string]string{"repository": "owner/repo", "ref": "main", "github_ref": "v1"},
			wantSubstr: "github_ref",
		},
		{
			name:       "plain http endpoint",
			attrs:      map[string]string{"repository": "owner/repo", "base_url": "http://ghe.example.com"},
			wantSubstr: "https",
		},
		{
			name:       "endpoint with credentials",
			attrs:      map[string]string{"repository": "owner/repo", "base_url": "https://user:pass@ghe.example.com"},
			wantSubstr: "credentials",
		},
		{
			name:       "github repository with too many segments",
			attrs:      map[string]string{"repository": "group/sub/project"},
			wantSubstr: "owner/repo",
		},
		{
			name:       "gitlab project without a namespace",
			attrs:      map[string]string{"platform": "gitlab", "repository": "project"},
			wantSubstr: "namespace",
		},
		// A left-over github_token on GitLab would send a GitHub credential to
		// the GitLab host as a private token.
		{
			name:       "github_token left behind on gitlab",
			attrs:      map[string]string{"platform": "gitlab", "repository": "group/project", "github_token": "ghp_x"},
			wantSubstr: "github_token is set while platform is",
		},
		{
			name:       "github_repo left behind on gitlab",
			attrs:      map[string]string{"platform": "gitlab", "github_repo": "group/project"},
			wantSubstr: "github_repo is set while platform is",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := configure(t, tt.attrs)
			if !resp.Diagnostics.HasError() {
				t.Fatal("expected an error diagnostic, got none")
			}
			var joined strings.Builder
			for _, d := range resp.Diagnostics.Errors() {
				joined.WriteString(d.Summary() + " " + d.Detail() + "\n")
			}
			if !strings.Contains(joined.String(), tt.wantSubstr) {
				t.Errorf("diagnostics %q do not mention %q", joined.String(), tt.wantSubstr)
			}
		})
	}
}

func TestCoalesce(t *testing.T) {
	tests := []struct {
		name       string
		current    types.String
		deprecated types.String
		want       string
		wantErr    bool
	}{
		{"current wins", types.StringValue("new"), types.StringNull(), "new", false},
		{"deprecated alone", types.StringNull(), types.StringValue("old"), "old", false},
		{"identical values agree", types.StringValue("same"), types.StringValue("same"), "same", false},
		{"both unset", types.StringNull(), types.StringNull(), "", false},
		{"unknown treated as unset", types.StringUnknown(), types.StringValue("old"), "old", false},
		{"conflict", types.StringValue("new"), types.StringValue("old"), "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := coalesce("repository", tt.current, "github_repo", tt.deprecated)
			if (err != nil) != tt.wantErr {
				t.Fatalf("coalesce() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("coalesce() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveGitHubToken_Precedence(t *testing.T) {
	t.Setenv(envToken, "")
	t.Setenv(envGHToken, "")
	t.Setenv(envGitHubToken, "")

	if got := resolveGitHubToken(context.Background(), "from-config", "https://api.github.com"); got != "from-config" {
		t.Errorf("configured token = %q, want it to win", got)
	}

	t.Setenv(envToken, "from-gkvm")
	if got := resolveGitHubToken(context.Background(), "", "https://api.github.com"); got != "from-gkvm" {
		t.Errorf("token = %q, want the neutral env var", got)
	}

	// GITHUB_TOKEN is injected into every GitHub Actions step and scoped to the
	// calling repository, so an explicit GH_TOKEN has to outrank it.
	t.Setenv(envToken, "")
	t.Setenv(envGHToken, "from-gh")
	t.Setenv(envGitHubToken, "from-actions")
	if got := resolveGitHubToken(context.Background(), "", "https://api.github.com"); got != "from-gh" {
		t.Errorf("token = %q, want GH_TOKEN to outrank GITHUB_TOKEN", got)
	}
}

func TestResolveGitLabToken_Precedence(t *testing.T) {
	t.Setenv(envToken, "")
	t.Setenv(envGitLabToken, "")
	t.Setenv(envCIJobToken, "")

	if got := resolveGitLabToken(context.Background(), "from-config", "https://gitlab.example.com/api/v4"); got != "from-config" {
		t.Errorf("token = %q, want the configured one to win", got)
	}

	t.Setenv(envGitLabToken, "from-gitlab")
	if got := resolveGitLabToken(context.Background(), "", "https://gitlab.example.com/api/v4"); got != "from-gitlab" {
		t.Errorf("token = %q, want GITLAB_TOKEN", got)
	}
}

// GitLab's job token allowlist does not cover the repository tree endpoint, and
// GitLab ignores the header there rather than rejecting it, so a job token
// would read as anonymous. It must not be picked up silently.
func TestResolveGitLabToken_IgnoresCIJobToken(t *testing.T) {
	t.Setenv(envToken, "")
	t.Setenv(envGitLabToken, "")
	t.Setenv(envCIJobToken, "from-ci")

	if got := resolveGitLabToken(context.Background(), "", "https://gitlab.example.com/api/v4"); got == "from-ci" {
		t.Error("CI_JOB_TOKEN was used as a token, but it cannot authenticate the tree endpoint")
	}

	help := gitlabTokenHelp("https://gitlab.example.com/api/v4")
	if !strings.Contains(help, envCIJobToken) || !strings.Contains(help, "allowlist") {
		t.Errorf("help does not explain why the job token is unusable: %s", help)
	}
}

func TestTokenHelp_NamesTheHostAndTheCommand(t *testing.T) {
	gh := githubTokenHelp("https://api.example.ghe.com")
	if !strings.Contains(gh, "example.ghe.com") || !strings.Contains(gh, "gh auth login") {
		t.Errorf("GitHub help is not actionable: %s", gh)
	}

	gl := gitlabTokenHelp("https://gitlab.example.com/api/v4")
	if !strings.Contains(gl, "gitlab.example.com") || !strings.Contains(gl, "glab auth login") {
		t.Errorf("GitLab help is not actionable: %s", gl)
	}
}
