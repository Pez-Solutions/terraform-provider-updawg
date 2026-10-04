package provider

import (
	"context"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework-timetypes/timetypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/oapi-codegen/nullable"

	"github.com/Pez-Solutions/terraform-provider-updawg/internal/client"
)

var (
	_ resource.ResourceWithConfigure   = (*enrollmentTokenResource)(nil)
	_ resource.ResourceWithImportState = (*enrollmentTokenResource)(nil)
)

func newEnrollmentTokenResource() resource.Resource {
	return &enrollmentTokenResource{}
}

type enrollmentTokenResource struct {
	data *providerData
}

type enrollmentTokenModel struct {
	Org       types.String      `tfsdk:"org"`
	ID        types.String      `tfsdk:"id"`
	Name      types.String      `tfsdk:"name"`
	Labels    types.Map         `tfsdk:"labels"`
	MaxUses   types.Int64       `tfsdk:"max_uses"`
	ExpiresAt timetypes.RFC3339 `tfsdk:"expires_at"`
	Token     types.String      `tfsdk:"token"`
	Uses      types.Int64       `tfsdk:"uses"`
	Usable    types.Bool        `tfsdk:"usable"`
}

func (r *enrollmentTokenResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_enrollment_token"
}

func (r *enrollmentTokenResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A token agents enroll with (`enr_…`). The API has no way to change one, so changing any " +
			"argument issues a new token and revokes the old; hosts already enrolled are unaffected. A token revoked in " +
			"the portal is planned for re-issue.\n\n" +
			"⚠️ **The value is stored in Terraform state.** The API shows it once, at creation, and keeps only a hash, " +
			"so state is the only place Terraform can keep it to hand to anything else. Treat state as a secret: an " +
			"encrypted, access-controlled backend. Limiting `max_uses` and setting `expires_at` bounds what a leaked " +
			"value is worth. Needs the token scope `enrollment`.",
		Attributes: map[string]schema.Attribute{
			"org": orgResourceAttribute(),
			"id":  idAttribute("The token's record id, `etk_…`. Not the credential."),
			"name": schema.StringAttribute{
				MarkdownDescription: "What it is for — `web tier`, `laptop test`.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"labels": schema.MapAttribute{
				MarkdownDescription: "Labels put on every host that enrolls with it.",
				ElementType:         types.StringType,
				Optional:            true,
				Validators:          []validator.Map{mapvalidator.SizeAtLeast(1)},
				PlanModifiers:       []planmodifier.Map{mapplanmodifier.RequiresReplace()},
			},
			"max_uses": schema.Int64Attribute{
				MarkdownDescription: "How many hosts it may enroll. Unlimited when left out. A use is counted at the host's " +
					"first check-in, not at enrollment.",
				Optional:      true,
				Validators:    []validator.Int64{int64validator.Between(1, 1<<31-1)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"expires_at": schema.StringAttribute{
				MarkdownDescription: "When it stops working, as RFC 3339 (`2027-01-01T00:00:00Z`). Never, when left out. " +
					"`timeadd(plantimestamp(), \"720h\")` would re-issue the token on every plan; use a fixed time or `time_rotating`.",
				CustomType:    timetypes.RFC3339Type{},
				Optional:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"token": schema.StringAttribute{
				MarkdownDescription: "The value an agent enrolls with. Known only to the configuration that created it: " +
					"an imported token has none.",
				Computed:      true,
				Sensitive:     true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"uses": schema.Int64Attribute{
				MarkdownDescription: "Hosts that enrolled with it and then checked in.",
				Computed:            true,
			},
			"usable": schema.BoolAttribute{
				MarkdownDescription: "Whether an agent presenting it now would be let in: not revoked, expired or used up.",
				Computed:            true,
			},
		},
	}
}

func (r *enrollmentTokenResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	data, diags := configured(req.ProviderData)
	resp.Diagnostics.Append(diags...)
	r.data = data
}

func (r *enrollmentTokenResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importOrgAndID(ctx, req, resp)
}

func (r *enrollmentTokenResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan enrollmentTokenModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	org, diags := resolveOrg(plan.Org, r.data)
	resp.Diagnostics.Append(diags...)
	labels, diags := toStringMap(ctx, plan.Labels)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := client.CreateTokenRequest{Name: plan.Name.ValueString()}
	if labels != nil {
		body.Labels = &labels
	}
	if !plan.MaxUses.IsNull() {
		body.MaxUses = nullable.NewNullableWithValue(int32(plan.MaxUses.ValueInt64()))
	}
	if !plan.ExpiresAt.IsNull() {
		t, d := plan.ExpiresAt.ValueRFC3339Time()
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}
		body.ExpiresAt = nullable.NewNullableWithValue(t)
	}

	res, err := r.data.client.EnrollmentTokensCreateWithResponse(ctx, org, body)
	if err != nil {
		resp.Diagnostics.AddError("Could not issue the enrollment token", err.Error())
		return
	}
	if res.JSON201 == nil {
		resp.Diagnostics.AddError("Could not issue the enrollment token", client.ProblemFrom(res.HTTPResponse, res.Body).Error())
		return
	}

	t := res.JSON201
	plan.Org = types.StringValue(org)
	plan.ID = types.StringValue(t.Id)
	plan.Token = types.StringValue(t.Token)
	plan.Uses = types.Int64Value(int64(t.Uses))
	plan.Usable = types.BoolValue(t.Usable)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *enrollmentTokenResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state enrollmentTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	org, diags := resolveOrg(state.Org, r.data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// There is no route for one token, so read the list.
	res, err := r.data.client.EnrollmentTokensListWithResponse(ctx, org)
	if err != nil {
		resp.Diagnostics.AddError("Could not read the enrollment token", err.Error())
		return
	}
	if res.StatusCode() == http.StatusNotFound {
		resp.State.RemoveResource(ctx)
		return
	}
	if res.JSON200 == nil {
		resp.Diagnostics.AddError("Could not read the enrollment token", client.ProblemFrom(res.HTTPResponse, res.Body).Error())
		return
	}

	var found *client.TokenBody
	for i := range *res.JSON200 {
		if (*res.JSON200)[i].Id == state.ID.ValueString() {
			found = &(*res.JSON200)[i]
			break
		}
	}
	// Revoked is as good as gone: it can never be used again, and the
	// configuration says there should be one that can.
	if found == nil || (found.RevokedAt.IsSpecified() && !found.RevokedAt.IsNull()) {
		resp.State.RemoveResource(ctx)
		return
	}

	state.Org = types.StringValue(org)
	state.Name = types.StringValue(found.Name)
	// The API answers {} for no labels; a configuration with none has null.
	if len(found.Labels) > 0 || !state.Labels.IsNull() {
		state.Labels = fromStringMap(found.Labels)
	}
	state.MaxUses = types.Int64Null()
	if found.MaxUses.IsSpecified() && !found.MaxUses.IsNull() {
		state.MaxUses = types.Int64Value(int64(found.MaxUses.MustGet()))
	}
	// RFC3339's semantic equality keeps the configuration's spelling of the
	// same instant, so the API's own formatting is not a change.
	state.ExpiresAt = timetypes.NewRFC3339Null()
	if found.ExpiresAt.IsSpecified() && !found.ExpiresAt.IsNull() {
		state.ExpiresAt = timetypes.NewRFC3339TimeValue(found.ExpiresAt.MustGet())
	}
	state.Uses = types.Int64Value(int64(found.Uses))
	state.Usable = types.BoolValue(found.Usable)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called: every argument requires replacement.
func (r *enrollmentTokenResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Enrollment tokens cannot be changed", "This is a bug in the provider: every argument should require replacement.")
}

func (r *enrollmentTokenResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state enrollmentTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	org, diags := resolveOrg(state.Org, r.data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	res, err := r.data.client.EnrollmentTokensRevokeWithResponse(ctx, org, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Could not revoke the enrollment token", err.Error())
		return
	}
	// 404 is also "already revoked".
	if res.StatusCode() != http.StatusNoContent && res.StatusCode() != http.StatusNotFound {
		resp.Diagnostics.AddError("Could not revoke the enrollment token", client.ProblemFrom(res.HTTPResponse, res.Body).Error())
	}
}
