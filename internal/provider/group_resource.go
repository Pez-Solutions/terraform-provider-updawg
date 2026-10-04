package provider

import (
	"context"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/oapi-codegen/nullable"

	"github.com/Pez-Solutions/terraform-provider-updawg/internal/client"
)

var (
	_ resource.ResourceWithConfigure   = (*groupResource)(nil)
	_ resource.ResourceWithImportState = (*groupResource)(nil)
)

func newGroupResource() resource.Resource {
	return &groupResource{}
}

type groupResource struct {
	data *providerData
}

type groupModel struct {
	Org           types.String `tfsdk:"org"`
	ID            types.String `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`
	Description   types.String `tfsdk:"description"`
	LabelSelector types.Map    `tfsdk:"label_selector"`
	Hosts         types.Set    `tfsdk:"hosts"`
	ResolvedHosts types.Int64  `tfsdk:"resolved_hosts"`
}

func (r *groupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group"
}

func (r *groupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A host group: hosts picked by label, by hand, or both. Policies select groups by **name**, " +
			"so renaming one leaves any policy naming the old name matching nothing of it; the provider warns when that happens. " +
			"Needs the token scopes `groups` and `read`.",
		Attributes: map[string]schema.Attribute{
			"org": orgResourceAttribute(),
			"id":  idAttribute("The group's id, `grp_…`."),
			"name": schema.StringAttribute{
				MarkdownDescription: "Unique in the organization. This is what a policy's `group:` selector names.",
				Required:            true,
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "What the group is for.",
				Optional:            true,
			},
			"label_selector": schema.MapAttribute{
				MarkdownDescription: "Labels a host must **all** carry, with exactly these values, to be in the group. " +
					"Leave it out for a group of only `hosts`. It cannot be empty: that would match every host.",
				ElementType: types.StringType,
				Optional:    true,
				Validators:  []validator.Map{mapvalidator.SizeAtLeast(1)},
			},
			"hosts": schema.SetAttribute{
				MarkdownDescription: "The hosts put in the group by hand, as `hst_…` ids — exactly these, replacing any " +
					"added in the portal. Leave it out to leave hand-picked membership to the portal; removing it later " +
					"stops managing it rather than emptying the group.",
				ElementType: types.StringType,
				Optional:    true,
			},
			"resolved_hosts": schema.Int64Attribute{
				MarkdownDescription: "How many hosts the group holds now, by hand or by label, each counted once.",
				Computed:            true,
			},
		},
	}
}

func (r *groupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	data, diags := configured(req.ProviderData)
	resp.Diagnostics.Append(diags...)
	r.data = data
}

func (r *groupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importOrgAndID(ctx, req, resp)
}

func (r *groupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan groupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	org, diags := resolveOrg(plan.Org, r.data)
	resp.Diagnostics.Append(diags...)
	selector, diags := toStringMap(ctx, plan.LabelSelector)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := client.CreateGroupRequest{Name: plan.Name.ValueString(), Description: toNullableString(plan.Description)}
	if selector != nil {
		body.LabelSelector = nullable.NewNullableWithValue(selector)
	}
	res, err := r.data.client.GroupsCreateWithResponse(ctx, org, body)
	if err != nil {
		resp.Diagnostics.AddError("Could not create the group", err.Error())
		return
	}
	if res.JSON201 == nil {
		resp.Diagnostics.AddError("Could not create the group", client.ProblemFrom(res.HTTPResponse, res.Body).Error())
		return
	}

	plan.Org = types.StringValue(org)
	plan.ID = types.StringValue(res.JSON201.Id)
	plan.ResolvedHosts = types.Int64Value(res.JSON201.ResolvedHosts)
	// Saved before the hosts are set, so a failure there leaves a tainted
	// group in state to be replaced, rather than one Terraform forgot.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)

	if !plan.Hosts.IsNull() {
		resp.Diagnostics.Append(r.setHosts(ctx, org, &plan)...)
		resp.Diagnostics.Append(r.refresh(ctx, org, &plan)...)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
}

func (r *groupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state groupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	org, diags := resolveOrg(state.Org, r.data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.Org = types.StringValue(org)

	gone, diags := r.read(ctx, org, &state)
	resp.Diagnostics.Append(diags...)
	if gone {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *groupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state groupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	org, diags := resolveOrg(plan.Org, r.data)
	resp.Diagnostics.Append(diags...)
	selector, diags := toStringMap(ctx, plan.LabelSelector)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Every field, every time: an absent field is left alone, so null is
	// how a description or selector removed from the configuration is
	// cleared.
	body := client.UpdateGroupRequest{
		Name:          nullable.NewNullableWithValue(plan.Name.ValueString()),
		Description:   toNullableString(plan.Description),
		LabelSelector: nullable.NewNullNullable[map[string]string](),
	}
	if selector != nil {
		body.LabelSelector = nullable.NewNullableWithValue(selector)
	}
	res, err := r.data.client.GroupsUpdateWithResponse(ctx, org, state.ID.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError("Could not update the group", err.Error())
		return
	}
	if res.JSON200 == nil {
		resp.Diagnostics.AddError("Could not update the group", client.ProblemFrom(res.HTTPResponse, res.Body).Error())
		return
	}
	if names := res.JSON200.PoliciesNamingOldName; len(names) > 0 {
		resp.Diagnostics.AddWarning("Policies still name the old group name",
			"These policies select the group by its old name, "+state.Name.ValueString()+", and now match nothing of it: "+
				strings.Join(names, ", ")+". Update their YAML to the new name.")
	}

	plan.ID = state.ID
	plan.Org = types.StringValue(org)
	if !plan.Hosts.IsNull() && !plan.Hosts.Equal(state.Hosts) {
		resp.Diagnostics.Append(r.setHosts(ctx, org, &plan)...)
	}
	resp.Diagnostics.Append(r.refresh(ctx, org, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *groupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state groupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	org, diags := resolveOrg(state.Org, r.data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	res, err := r.data.client.GroupsDeleteWithResponse(ctx, org, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Could not delete the group", err.Error())
		return
	}
	if res.StatusCode() == http.StatusNotFound {
		return
	}
	if res.JSON200 == nil {
		resp.Diagnostics.AddError("Could not delete the group", client.ProblemFrom(res.HTTPResponse, res.Body).Error())
		return
	}
	if names := res.JSON200.PoliciesNamingIt; len(names) > 0 {
		resp.Diagnostics.AddWarning("Policies still name the deleted group",
			"These policies select "+state.Name.ValueString()+" by name and now match nothing of it: "+strings.Join(names, ", ")+".")
	}
}

func (r *groupResource) setHosts(ctx context.Context, org string, m *groupModel) diag.Diagnostics {
	hosts, diags := toStringSlice(ctx, m.Hosts)
	if diags.HasError() {
		return diags
	}
	res, err := r.data.client.GroupsSetHostsWithResponse(ctx, org, m.ID.ValueString(), client.SetHostsRequest{Hosts: hosts})
	if err != nil {
		diags.AddError("Could not set the group's hosts", err.Error())
		return diags
	}
	if res.JSON200 == nil {
		diags.AddError("Could not set the group's hosts", client.ProblemFrom(res.HTTPResponse, res.Body).Error())
	}
	return diags
}

// refresh reads back what the API now says, for the computed count after a
// write. A group that vanished in between is an error here, not drift.
func (r *groupResource) refresh(ctx context.Context, org string, m *groupModel) diag.Diagnostics {
	gone, diags := r.read(ctx, org, m)
	if gone {
		diags.AddError("The group disappeared", "It was deleted while Terraform was changing it.")
	}
	return diags
}

// read fills m from the API, reporting whether the group is gone.
func (r *groupResource) read(ctx context.Context, org string, m *groupModel) (bool, diag.Diagnostics) {
	var diags diag.Diagnostics
	res, err := r.data.client.GroupsShowWithResponse(ctx, org, m.ID.ValueString())
	if err != nil {
		diags.AddError("Could not read the group", err.Error())
		return false, diags
	}
	if res.StatusCode() == http.StatusNotFound {
		return true, diags
	}
	if res.JSON200 == nil {
		diags.AddError("Could not read the group", client.ProblemFrom(res.HTTPResponse, res.Body).Error())
		return false, diags
	}

	g := res.JSON200
	m.Name = types.StringValue(g.Name)
	m.Description = fromNullableString(g.Description)
	m.LabelSelector = types.MapNull(types.StringType)
	if g.LabelSelector.IsSpecified() && !g.LabelSelector.IsNull() {
		m.LabelSelector = fromStringMap(g.LabelSelector.MustGet())
	}
	m.ResolvedHosts = types.Int64Value(g.ResolvedHosts)
	// Hand-picked membership is only read back when the configuration
	// manages it; otherwise the portal's additions would show as drift on
	// an attribute nobody set.
	if !m.Hosts.IsNull() {
		static := []string{}
		for _, h := range g.Hosts {
			if h.Static {
				static = append(static, h.Id)
			}
		}
		m.Hosts = fromStringSlice(static)
	}
	return false, diags
}
