package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// TestProviderSchemas exercises schema construction and framework-level
// validation for the provider and every resource/data source.
func TestProviderSchemas(t *testing.T) {
	ctx := context.Background()
	srv := providerserver.NewProtocol6(New("test")())()

	resp, err := srv.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Errorf("schema diagnostic: %s: %s", d.Summary, d.Detail)
		}
	}

	wantResources := []string{
		"agentsandbox_install",
		"agentsandbox_sandbox",
		"agentsandbox_sandbox_template",
		"agentsandbox_sandbox_warmpool",
		"agentsandbox_sandbox_claim",
	}
	for _, name := range wantResources {
		if _, ok := resp.ResourceSchemas[name]; !ok {
			t.Errorf("resource %s not registered", name)
		}
	}
	if _, ok := resp.DataSourceSchemas["agentsandbox_sandbox"]; !ok {
		t.Error("data source agentsandbox_sandbox not registered")
	}
}
