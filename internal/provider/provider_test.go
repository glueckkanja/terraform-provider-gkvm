package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
)

func TestGkvmProvider_Metadata(t *testing.T) {
	p := &GkvmProvider{version: "1.2.3"}
	req := provider.MetadataRequest{}
	resp := &provider.MetadataResponse{}
	p.Metadata(context.Background(), req, resp)

	if resp.TypeName != "gkvm" {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, "gkvm")
	}
	if resp.Version != "1.2.3" {
		t.Errorf("Version = %q, want %q", resp.Version, "1.2.3")
	}
}

func TestGkvmProvider_Schema(t *testing.T) {
	p := &GkvmProvider{}
	req := provider.SchemaRequest{}
	resp := &provider.SchemaResponse{}
	p.Schema(context.Background(), req, resp)

	attrs := resp.Schema.Attributes
	if _, ok := attrs["github_repo"]; !ok {
		t.Error("schema missing github_repo attribute")
	}
	if _, ok := attrs["github_ref"]; !ok {
		t.Error("schema missing github_ref attribute")
	}
	if _, ok := attrs["github_token"]; !ok {
		t.Error("schema missing github_token attribute")
	}
}

func TestGkvmProvider_DataSources(t *testing.T) {
	p := &GkvmProvider{}
	sources := p.DataSources(context.Background())
	if len(sources) == 0 {
		t.Error("expected at least one data source, got none")
	}
}

func TestGkvmProvider_Resources(t *testing.T) {
	p := &GkvmProvider{}
	resources := p.Resources(context.Background())
	if resources != nil {
		t.Errorf("expected nil resources, got %v", resources)
	}
}

func TestNew_ReturnsProvider(t *testing.T) {
	factory := New("0.1.0")
	p := factory()
	if p == nil {
		t.Fatal("New() returned nil")
	}
}
