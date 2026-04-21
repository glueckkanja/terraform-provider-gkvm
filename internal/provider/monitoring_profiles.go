package provider

import (
	"context"
	"sort"
	"strings"

	"github.com/glueckkanja/terraform-provider-gkvm/internal/monitoring"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSource = &MonitoringProfilesDataSource{}

type MonitoringProfilesDataSource struct {
	providerData *providerData
}

type monitoringProfilesModel struct {
	ProfilePath  types.String `tfsdk:"profile_path"`
	Filter       types.String `tfsdk:"filter"`
	Profiles     types.Map    `tfsdk:"profiles"`
	ProfileNames types.List   `tfsdk:"profile_names"`
}

func NewMonitoringProfilesDataSource() datasource.DataSource {
	return &MonitoringProfilesDataSource{}
}

func (d *MonitoringProfilesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_monitoring_profiles"
}

func (d *MonitoringProfilesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Fetches monitoring alert profiles from the configured GitHub repository. Each profile contains metric_alerts and log_alerts as a JSON string. Use jsondecode() to parse.",
		Attributes: map[string]schema.Attribute{
			"profile_path": schema.StringAttribute{
				Optional:    true,
				Description: "Directory path within the repository containing YAML profiles. Defaults to \"defaults\".",
			},
			"filter": schema.StringAttribute{
				Optional:    true,
				Description: "Comma-separated list of profile names to return. If empty, all profiles are returned.",
			},
			"profiles": schema.MapAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Map of profile_name to JSON string containing {metric_alerts: {...}, log_alerts: {...}}.",
			},
			"profile_names": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "List of available profile names.",
			},
		},
	}
}

func (d *MonitoringProfilesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	pd, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type", "Expected *providerData")
		return
	}
	d.providerData = pd
}

func (d *MonitoringProfilesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var model monitoringProfilesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if d.providerData == nil || d.providerData.GitHubClient == nil {
		resp.Diagnostics.AddError("Provider not configured", "The gkvm provider must be configured with a github_repo before using this data source.")
		return
	}

	profilePath := ""
	if !model.ProfilePath.IsNull() && !model.ProfilePath.IsUnknown() {
		profilePath = model.ProfilePath.ValueString()
	}

	allProfiles, err := monitoring.FetchProfiles(d.providerData.GitHubClient, profilePath)
	if err != nil {
		resp.Diagnostics.AddError("Failed to fetch monitoring profiles", err.Error())
		return
	}

	// Apply filter if set
	filtered := allProfiles
	if !model.Filter.IsNull() && !model.Filter.IsUnknown() && model.Filter.ValueString() != "" {
		filterNames := strings.Split(model.Filter.ValueString(), ",")
		filterSet := make(map[string]bool)
		for _, name := range filterNames {
			filterSet[strings.TrimSpace(name)] = true
		}

		filtered = make(map[string]string)
		for name, content := range allProfiles {
			if filterSet[name] {
				filtered[name] = content
			}
		}
	}

	// Build profiles map
	profileValues := make(map[string]attr.Value, len(filtered))
	for name, content := range filtered {
		profileValues[name] = types.StringValue(content)
	}
	model.Profiles = types.MapValueMust(types.StringType, profileValues)

	// Build profile_names list (all profiles, sorted for deterministic state)
	sortedNames := make([]string, 0, len(allProfiles))
	for name := range allProfiles {
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)

	nameValues := make([]attr.Value, 0, len(sortedNames))
	for _, name := range sortedNames {
		nameValues = append(nameValues, types.StringValue(name))
	}
	model.ProfileNames = types.ListValueMust(types.StringType, nameValues)

	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}
