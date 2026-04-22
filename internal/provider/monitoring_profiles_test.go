package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
)

func TestMonitoringProfilesDataSource_Metadata(t *testing.T) {
	ds := &MonitoringProfilesDataSource{}
	req := datasource.MetadataRequest{ProviderTypeName: "gkvm"}
	resp := &datasource.MetadataResponse{}
	ds.Metadata(context.Background(), req, resp)

	if resp.TypeName != "gkvm_monitoring_profiles" {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, "gkvm_monitoring_profiles")
	}
}

func TestMonitoringProfilesDataSource_Schema(t *testing.T) {
	ds := &MonitoringProfilesDataSource{}
	req := datasource.SchemaRequest{}
	resp := &datasource.SchemaResponse{}
	ds.Schema(context.Background(), req, resp)

	attrs := resp.Schema.Attributes
	for _, required := range []string{"profile_path", "filter", "profiles", "profile_names"} {
		if _, ok := attrs[required]; !ok {
			t.Errorf("schema missing attribute %q", required)
		}
	}
}

func TestMonitoringProfilesDataSource_Configure_NilProviderData(t *testing.T) {
	ds := &MonitoringProfilesDataSource{}
	req := datasource.ConfigureRequest{ProviderData: nil}
	resp := &datasource.ConfigureResponse{}
	ds.Configure(context.Background(), req, resp)

	if resp.Diagnostics.HasError() {
		t.Errorf("Configure with nil ProviderData should not error, got: %v", resp.Diagnostics)
	}
}

func TestMonitoringProfilesDataSource_Configure_WrongType(t *testing.T) {
	ds := &MonitoringProfilesDataSource{}
	req := datasource.ConfigureRequest{ProviderData: "not-a-providerData"}
	resp := &datasource.ConfigureResponse{}
	ds.Configure(context.Background(), req, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("Configure with wrong type should produce an error diagnostic")
	}
}

func TestNewMonitoringProfilesDataSource(t *testing.T) {
	ds := NewMonitoringProfilesDataSource()
	if ds == nil {
		t.Fatal("NewMonitoringProfilesDataSource() returned nil")
	}
}
