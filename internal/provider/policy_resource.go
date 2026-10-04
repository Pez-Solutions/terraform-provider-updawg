package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/oapi-codegen/nullable"

	"github.com/Pez-Solutions/terraform-provider-updawg/internal/client"
)

var (
	_ resource.ResourceWithConfigure   = (*policyResource)(nil)
	_ resource.ResourceWithImportState = (*policyResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*policyResource)(nil)
)

func newPolicyResource() resource.Resource {
	return &policyResource{}
}

type policyResource struct {
	data *providerData
}

type policyModel struct {
	Org      types.String `tfsdk:"org"`
	ID       types.String `tfsdk:"id"`
	YAML     types.String `tfsdk:"yaml"`
	Name     types.String `tfsdk:"name"`
	Priority types.Int64  `tfsdk:"priority"`
	Enabled  types.Bool   `tfsdk:"enabled"`
	Version  types.Int64  `tfsdk:"version"`
}

func (r *policyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_policy"
}

func (r *policyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A policy, as the YAML document the portal's editor shows — the same text, validated by the API " +
			"during `plan`. Its name, priority and whether it is enabled are read from the document. Every change is a new " +
			"version; an edit made in the portal shows as a difference in `yaml` on the next plan, and applying puts the " +
			"configuration's text back as a newer version. Deleting it is a soft delete: proposals it made keep their reason. " +
			"Needs the token scopes `policy` and `read`.",
		Attributes: map[string]schema.Attribute{
			"org": orgResourceAttribute(),
			"id":  idAttribute("The policy's id, `pol_…`."),
			"yaml": schema.StringAttribute{
				MarkdownDescription: "The policy document. Kept byte for byte, so whitespace and comments are part of it. " +
					"`file()` or a heredoc keeps it readable. See [policies](https://docs.updawg.net/policies/) for what it says.",
				Required: true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The document's `name`, unique among live policies.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"priority": schema.Int64Attribute{
				MarkdownDescription: "The document's `priority`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"enabled": schema.BoolAttribute{
				MarkdownDescription: "The document's `enabled`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"version": schema.Int64Attribute{
				MarkdownDescription: "The version in force. Each apply that changes `yaml` makes a new one.",
				Computed:            true,
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *policyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	data, diags := configured(req.ProviderData)
	resp.Diagnostics.Append(diags...)
	r.data = data
}

func (r *policyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importOrgAndID(ctx, req, resp)
}

// ModifyPlan validates a changed document against the API, so a policy that
// does not compile fails `plan` with the line it fails on rather than
// `apply` halfway through. The plan then shows the name, priority and
// enabled the document will have.
//
// When the API cannot be asked — unreachable, or a token without the
// `policy` scope — the plan goes ahead with a warning: apply validates again
// regardless, and a plan should not depend on the network more than it must.
func (r *policyResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.data == nil {
		return
	}
	var plan, state policyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if !req.State.Raw.IsNull() {
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	}
	if resp.Diagnostics.HasError() || plan.YAML.IsUnknown() || plan.YAML.Equal(state.YAML) {
		return
	}
	org, diags := resolveOrg(plan.Org, r.data)
	if diags.HasError() {
		// Not this method's error to report: Create says the same thing.
		return
	}

	// A changed document is a new version, whatever the API says about it.
	plan.Version = types.Int64Unknown()

	body := client.PolicyRequest{Yaml: nullable.NewNullableWithValue(plan.YAML.ValueString())}
	res, err := r.data.client.PoliciesValidateWithResponse(ctx, org, body)
	switch {
	case err != nil:
		resp.Diagnostics.AddWarning("The policy was not validated", "The API could not be asked: "+err.Error()+". Apply validates it again.")
		plan.Name, plan.Priority, plan.Enabled = types.StringUnknown(), types.Int64Unknown(), types.BoolUnknown()
	case res.JSON200 == nil:
		resp.Diagnostics.AddWarning("The policy was not validated", client.ProblemFrom(res.HTTPResponse, res.Body).Error()+". Apply validates it again.")
		plan.Name, plan.Priority, plan.Enabled = types.StringUnknown(), types.Int64Unknown(), types.BoolUnknown()
	case !res.JSON200.Valid:
		for _, p := range res.JSON200.Errors {
			resp.Diagnostics.AddAttributeError(path.Root("yaml"), "The policy does not compile", policyProblem(p))
		}
		if len(res.JSON200.Errors) == 0 {
			resp.Diagnostics.AddAttributeError(path.Root("yaml"), "The policy does not compile", "The API gave no reason.")
		}
		return
	default:
		v := res.JSON200
		r.preview(ctx, org, plan.YAML.ValueString(), state.ID, resp)
		if resp.Diagnostics.HasError() {
			return
		}
		plan.Name = fromNullableString(v.Name)
		plan.Priority = types.Int64Unknown()
		if v.Priority.IsSpecified() && !v.Priority.IsNull() {
			plan.Priority = types.Int64Value(int64(v.Priority.MustGet()))
		}
		plan.Enabled = types.BoolUnknown()
		if v.Enabled.IsSpecified() && !v.Enabled.IsNull() {
			plan.Enabled = types.BoolValue(v.Enabled.MustGet())
		}
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

// preview asks the API what saving the document would do to the fleet and
// puts the answer in the plan as a warning (DAWG-138) — the one place a
// plan can say something in words. A preview that cannot be had is a
// warning too, never a failed plan: it informs a decision, it is not a
// check. The one exception is a name another live policy holds, which is
// what saving would answer as well.
func (r *policyResource) preview(ctx context.Context, org, yaml string, replaces types.String, resp *resource.ModifyPlanResponse) {
	body := client.PreviewRequest{Yaml: nullable.NewNullableWithValue(yaml)}
	if !replaces.IsNull() && !replaces.IsUnknown() {
		body.Replaces = nullable.NewNullableWithValue(replaces.ValueString())
	}
	res, err := r.data.client.PoliciesPreviewWithResponse(ctx, org, body)
	switch {
	case err != nil:
		resp.Diagnostics.AddWarning("No preview of this policy change", "The API could not be asked: "+err.Error())
	case res.StatusCode() == http.StatusConflict:
		resp.Diagnostics.AddAttributeError(path.Root("yaml"), "The policy's name is taken", client.ProblemFrom(res.HTTPResponse, res.Body).Error())
	case res.JSON200 == nil:
		resp.Diagnostics.AddWarning("No preview of this policy change", client.ProblemFrom(res.HTTPResponse, res.Body).Error())
	default:
		if summary, ok := previewSummary(res.JSON200); ok {
			resp.Diagnostics.AddAttributeWarning(path.Root("yaml"), "What this policy change would do", summary)
		}
	}
}

// policyProblem puts the line and column in front of the API's message, the
// way a compiler would.
func policyProblem(p client.PolicyProblem) string {
	var at []string
	if p.Line.IsSpecified() && !p.Line.IsNull() {
		at = append(at, fmt.Sprintf("line %d", p.Line.MustGet()))
	}
	if p.Column.IsSpecified() && !p.Column.IsNull() {
		at = append(at, fmt.Sprintf("column %d", p.Column.MustGet()))
	}
	if len(at) == 0 {
		return p.Detail
	}
	return strings.Join(at, ", ") + ": " + p.Detail
}

func (r *policyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan policyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	org, diags := resolveOrg(plan.Org, r.data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	res, err := r.data.client.PoliciesCreateWithResponse(ctx, org, client.PolicyRequest{Yaml: nullable.NewNullableWithValue(plan.YAML.ValueString())})
	if err != nil {
		resp.Diagnostics.AddError("Could not create the policy", err.Error())
		return
	}
	if res.JSON201 == nil {
		resp.Diagnostics.AddError("Could not create the policy", client.ProblemFrom(res.HTTPResponse, res.Body).Error())
		return
	}
	plan.Org = types.StringValue(org)
	fillPolicy(&plan, res.JSON201)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *policyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state policyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	org, diags := resolveOrg(state.Org, r.data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	res, err := r.data.client.PoliciesShowWithResponse(ctx, org, state.ID.ValueString(), nil)
	if err != nil {
		resp.Diagnostics.AddError("Could not read the policy", err.Error())
		return
	}
	// Deleted policies answer 404 too, so one deleted in the portal is
	// planned for re-creation.
	if res.StatusCode() == http.StatusNotFound {
		resp.State.RemoveResource(ctx)
		return
	}
	if res.JSON200 == nil {
		resp.Diagnostics.AddError("Could not read the policy", client.ProblemFrom(res.HTTPResponse, res.Body).Error())
		return
	}
	state.Org = types.StringValue(org)
	fillPolicy(&state, res.JSON200)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *policyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state policyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	org, diags := resolveOrg(plan.Org, r.data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	res, err := r.data.client.PoliciesUpdateWithResponse(ctx, org, state.ID.ValueString(), client.PolicyRequest{Yaml: nullable.NewNullableWithValue(plan.YAML.ValueString())})
	if err != nil {
		resp.Diagnostics.AddError("Could not update the policy", err.Error())
		return
	}
	if res.JSON200 == nil {
		resp.Diagnostics.AddError("Could not update the policy", client.ProblemFrom(res.HTTPResponse, res.Body).Error())
		return
	}
	plan.Org = types.StringValue(org)
	fillPolicy(&plan, res.JSON200)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *policyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state policyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	org, diags := resolveOrg(state.Org, r.data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	res, err := r.data.client.PoliciesRemoveWithResponse(ctx, org, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Could not delete the policy", err.Error())
		return
	}
	if res.StatusCode() != http.StatusNoContent && res.StatusCode() != http.StatusNotFound {
		resp.Diagnostics.AddError("Could not delete the policy", client.ProblemFrom(res.HTTPResponse, res.Body).Error())
	}
}

func fillPolicy(m *policyModel, d *client.Detail) {
	m.ID = types.StringValue(d.Id)
	m.YAML = types.StringValue(d.Yaml)
	m.Name = types.StringValue(d.Name)
	m.Priority = types.Int64Value(int64(d.Priority))
	m.Enabled = types.BoolValue(d.Enabled)
	m.Version = types.Int64Value(int64(d.Version))
}
