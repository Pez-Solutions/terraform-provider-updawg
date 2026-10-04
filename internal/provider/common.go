package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/oapi-codegen/nullable"
)

// orgResourceAttribute is every resource's `org`. Moving a resource to
// another organization is a new resource there: nothing in the API moves
// one.
func orgResourceAttribute() schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: orgAttribute + " Changing it replaces the resource.",
		Optional:            true,
		Computed:            true,
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.RequiresReplaceIfConfigured(),
			stringplanmodifier.UseStateForUnknown(),
		},
	}
}

// idAttribute is every resource's `id`, which the API assigns once.
func idAttribute(what string) schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: what,
		Computed:            true,
		PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
}

// importOrgAndID takes an import id of `org/id`, or a bare `id` in the
// provider's organization.
func importOrgAndID(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := req.ID
	if org, rest, ok := strings.Cut(req.ID, "/"); ok {
		if org == "" || rest == "" {
			resp.Diagnostics.AddError("Unreadable import id", "Expected org/id or id, got "+req.ID+".")
			return
		}
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("org"), org)...)
		id = rest
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

// The API's nullable fields and Terraform's null are the same idea. These
// move values between them.

func toNullableString(v types.String) nullable.Nullable[string] {
	if v.IsNull() || v.IsUnknown() {
		return nullable.NewNullNullable[string]()
	}
	return nullable.NewNullableWithValue(v.ValueString())
}

func fromNullableString(n nullable.Nullable[string]) types.String {
	if !n.IsSpecified() || n.IsNull() {
		return types.StringNull()
	}
	return types.StringValue(n.MustGet())
}

func toStringMap(ctx context.Context, v types.Map) (map[string]string, diag.Diagnostics) {
	if v.IsNull() || v.IsUnknown() {
		return nil, nil
	}
	out := map[string]string{}
	diags := v.ElementsAs(ctx, &out, false)
	return out, diags
}

func fromStringMap(m map[string]string) types.Map {
	if m == nil {
		return types.MapNull(types.StringType)
	}
	elems := make(map[string]attr.Value, len(m))
	for k, v := range m {
		elems[k] = types.StringValue(v)
	}
	return types.MapValueMust(types.StringType, elems)
}

func toStringSlice(ctx context.Context, v types.Set) ([]string, diag.Diagnostics) {
	out := []string{}
	if v.IsNull() || v.IsUnknown() {
		return out, nil
	}
	diags := v.ElementsAs(ctx, &out, false)
	return out, diags
}

func fromStringSlice(s []string) types.Set {
	elems := make([]attr.Value, len(s))
	for i, v := range s {
		elems[i] = types.StringValue(v)
	}
	return types.SetValueMust(types.StringType, elems)
}
