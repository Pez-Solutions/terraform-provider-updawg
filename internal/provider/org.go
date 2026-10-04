package provider

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// orgAttribute is the description of every resource's and data source's
// `org`, so they all say the same thing.
const orgAttribute = "The organization's slug. Defaults to the provider's `org`."

// resolveOrg picks the organization a resource or data source acts on: its
// own `org` when set, otherwise the provider's.
func resolveOrg(own types.String, data *providerData) (string, diag.Diagnostics) {
	var diags diag.Diagnostics
	if org := own.ValueString(); org != "" {
		return org, diags
	}
	if data.org != "" {
		return data.org, diags
	}
	diags.AddAttributeError(path.Root("org"), "No organization",
		fmt.Sprintf("Set org here, org in the provider block, or %s in the environment.", envOrg))
	return "", diags
}

// configured unpacks what Configure handed a resource or data source. It is
// nil before the provider has been configured, which Terraform does during
// validation; callers return early then.
func configured(v any) (*providerData, diag.Diagnostics) {
	var diags diag.Diagnostics
	if v == nil {
		return nil, diags
	}
	data, ok := v.(*providerData)
	if !ok {
		diags.AddError("Unexpected provider data", fmt.Sprintf("Expected *providerData, got %T. This is a bug in the provider.", v))
	}
	return data, diags
}
