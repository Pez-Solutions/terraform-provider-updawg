package provider

import (
	"context"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/oapi-codegen/nullable"

	"github.com/Pez-Solutions/terraform-provider-updawg/internal/client"
)

var (
	_ resource.ResourceWithConfigure   = (*ruleResource)(nil)
	_ resource.ResourceWithImportState = (*ruleResource)(nil)
)

// eventTypes are what a rule may subscribe to (CreateRuleRequest.event_types).
var eventTypes = []string{
	"proposal.opened", "proposal.auto_merged", "proposal.approved", "proposal.merged",
	"proposal.halted", "host.stale", "host.recovered", "release.eol",
}

var filterAttrTypes = map[string]attr.Type{
	"min_severity":    types.StringType,
	"known_exploited": types.BoolType,
	"proposal_kinds":  types.SetType{ElemType: types.StringType},
}

func newRuleResource() resource.Resource {
	return &ruleResource{}
}

type ruleResource struct {
	data *providerData
}

type ruleModel struct {
	Org        types.String `tfsdk:"org"`
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	EventTypes types.Set    `tfsdk:"event_types"`
	ChannelIDs types.Set    `tfsdk:"channel_ids"`
	Enabled    types.Bool   `tfsdk:"enabled"`
	Filter     types.Object `tfsdk:"filter"`
}

type filterModel struct {
	MinSeverity    types.String `tfsdk:"min_severity"`
	KnownExploited types.Bool   `tfsdk:"known_exploited"`
	ProposalKinds  types.Set    `tfsdk:"proposal_kinds"`
}

func (r *ruleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_notification_rule"
}

func (r *ruleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Which events go to which channels. A rule for `proposal.auto_merged` with no channels is " +
			"how auto-merges are silenced. Needs the token scope `integrations`.",
		Attributes: map[string]schema.Attribute{
			"org": orgResourceAttribute(),
			"id":  idAttribute("The rule's id, `nru_…`."),
			"name": schema.StringAttribute{
				MarkdownDescription: "Unique in the organization.",
				Required:            true,
			},
			"event_types": schema.SetAttribute{
				MarkdownDescription: "Any of `proposal.opened`, `proposal.auto_merged`, `proposal.approved`, " +
					"`proposal.merged`, `proposal.halted`, `host.stale`, `host.recovered`, `release.eol`.",
				ElementType: types.StringType,
				Required:    true,
				Validators: []validator.Set{
					setvalidator.SizeAtLeast(1),
					setvalidator.ValueStringsAre(stringvalidator.OneOf(eventTypes...)),
				},
			},
			"channel_ids": schema.SetAttribute{
				MarkdownDescription: "`updawg_notification_channel` ids. May be empty.",
				ElementType:         types.StringType,
				Required:            true,
			},
			"enabled": schema.BoolAttribute{
				MarkdownDescription: "Defaults to `true`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
			},
			"filter": schema.SingleNestedAttribute{
				MarkdownDescription: "Narrows the events; every field given must hold. The proposal fields do not narrow host events.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"min_severity": schema.StringAttribute{
						MarkdownDescription: "At least this severe: `none`, `low`, `medium`, `high` or `critical`.",
						Optional:            true,
						Validators: []validator.String{stringvalidator.OneOf(
							string(client.SeverityNone), string(client.SeverityLow), string(client.SeverityMedium),
							string(client.SeverityHigh), string(client.SeverityCritical),
						)},
					},
					"known_exploited": schema.BoolAttribute{
						MarkdownDescription: "Only proposals that fix something known to be exploited (`true`), or only those that do not (`false`).",
						Optional:            true,
					},
					"proposal_kinds": schema.SetAttribute{
						MarkdownDescription: "Only these kinds of proposal: `security`, `bugfix`, …",
						ElementType:         types.StringType,
						Optional:            true,
					},
				},
			},
		},
	}
}

func (r *ruleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	data, diags := configured(req.ProviderData)
	resp.Diagnostics.Append(diags...)
	r.data = data
}

func (r *ruleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importOrgAndID(ctx, req, resp)
}

// toFilter is the filter to send. No filter is an empty one: on an update
// an absent filter leaves the stored one alone, so `{}` is how one removed
// from the configuration is cleared.
func toFilter(ctx context.Context, v types.Object) (client.FilterBody, diag.Diagnostics) {
	var out client.FilterBody
	if v.IsNull() || v.IsUnknown() {
		return out, nil
	}
	var f filterModel
	diags := v.As(ctx, &f, basetypes.ObjectAsOptions{})
	if diags.HasError() {
		return out, diags
	}
	if !f.MinSeverity.IsNull() {
		out.MinSeverity = nullable.NewNullableWithValue(client.Severity(f.MinSeverity.ValueString()))
	}
	if !f.KnownExploited.IsNull() {
		out.KnownExploited = nullable.NewNullableWithValue(f.KnownExploited.ValueBool())
	}
	if !f.ProposalKinds.IsNull() {
		kinds, d := toStringSlice(ctx, f.ProposalKinds)
		diags.Append(d...)
		out.ProposalKinds = nullable.NewNullableWithValue(kinds)
	}
	return out, diags
}

// fromFilter is the filter as state holds it. The API always answers with
// a filter object, all null for none; a configuration with no filter has
// null, and one with `filter = {}` keeps its empty object.
func fromFilter(f client.FilterBody, prior types.Object) types.Object {
	minSev := types.StringNull()
	if f.MinSeverity.IsSpecified() && !f.MinSeverity.IsNull() {
		minSev = types.StringValue(string(f.MinSeverity.MustGet()))
	}
	exploited := types.BoolNull()
	if f.KnownExploited.IsSpecified() && !f.KnownExploited.IsNull() {
		exploited = types.BoolValue(f.KnownExploited.MustGet())
	}
	kinds := types.SetNull(types.StringType)
	if f.ProposalKinds.IsSpecified() && !f.ProposalKinds.IsNull() {
		kinds = fromStringSlice(f.ProposalKinds.MustGet())
	}
	if minSev.IsNull() && exploited.IsNull() && kinds.IsNull() && prior.IsNull() {
		return types.ObjectNull(filterAttrTypes)
	}
	return types.ObjectValueMust(filterAttrTypes, map[string]attr.Value{
		"min_severity": minSev, "known_exploited": exploited, "proposal_kinds": kinds,
	})
}

func (r *ruleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ruleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	org, diags := resolveOrg(plan.Org, r.data)
	resp.Diagnostics.Append(diags...)
	events, diags := toStringSlice(ctx, plan.EventTypes)
	resp.Diagnostics.Append(diags...)
	channels, diags := toStringSlice(ctx, plan.ChannelIDs)
	resp.Diagnostics.Append(diags...)
	filter, diags := toFilter(ctx, plan.Filter)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	enabled := plan.Enabled.ValueBool()
	res, err := r.data.client.NotificationRulesCreateWithResponse(ctx, org, client.CreateRuleRequest{
		Name: plan.Name.ValueString(), EventTypes: events, ChannelIds: channels, Enabled: &enabled, Filter: &filter,
	})
	if err != nil {
		resp.Diagnostics.AddError("Could not create the rule", err.Error())
		return
	}
	if res.JSON201 == nil {
		resp.Diagnostics.AddError("Could not create the rule", client.ProblemFrom(res.HTTPResponse, res.Body).Error())
		return
	}
	plan.Org = types.StringValue(org)
	fillRule(&plan, res.JSON201)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ruleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ruleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	org, diags := resolveOrg(state.Org, r.data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	res, err := r.data.client.NotificationRulesListWithResponse(ctx, org)
	if err != nil {
		resp.Diagnostics.AddError("Could not read the rule", err.Error())
		return
	}
	if res.StatusCode() == http.StatusNotFound {
		resp.State.RemoveResource(ctx)
		return
	}
	if res.JSON200 == nil {
		resp.Diagnostics.AddError("Could not read the rule", client.ProblemFrom(res.HTTPResponse, res.Body).Error())
		return
	}
	for i := range *res.JSON200 {
		if rule := &(*res.JSON200)[i]; rule.Id == state.ID.ValueString() {
			state.Org = types.StringValue(org)
			fillRule(&state, rule)
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
	}
	// Deleting a channel deletes the rules that sent to it, so a rule can
	// vanish without anybody touching it.
	resp.State.RemoveResource(ctx)
}

func (r *ruleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state ruleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	org, diags := resolveOrg(plan.Org, r.data)
	resp.Diagnostics.Append(diags...)
	events, diags := toStringSlice(ctx, plan.EventTypes)
	resp.Diagnostics.Append(diags...)
	channels, diags := toStringSlice(ctx, plan.ChannelIDs)
	resp.Diagnostics.Append(diags...)
	filter, diags := toFilter(ctx, plan.Filter)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	res, err := r.data.client.NotificationRulesUpdateWithResponse(ctx, org, state.ID.ValueString(), client.UpdateRuleRequest{
		Name:       nullable.NewNullableWithValue(plan.Name.ValueString()),
		EventTypes: nullable.NewNullableWithValue(events),
		ChannelIds: nullable.NewNullableWithValue(channels),
		Enabled:    nullable.NewNullableWithValue(plan.Enabled.ValueBool()),
		Filter:     nullable.NewNullableWithValue(filter),
	})
	if err != nil {
		resp.Diagnostics.AddError("Could not update the rule", err.Error())
		return
	}
	if res.JSON200 == nil {
		resp.Diagnostics.AddError("Could not update the rule", client.ProblemFrom(res.HTTPResponse, res.Body).Error())
		return
	}
	plan.Org = types.StringValue(org)
	fillRule(&plan, res.JSON200)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ruleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ruleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	org, diags := resolveOrg(state.Org, r.data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	res, err := r.data.client.NotificationRulesDeleteWithResponse(ctx, org, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Could not delete the rule", err.Error())
		return
	}
	if res.StatusCode() != http.StatusNoContent && res.StatusCode() != http.StatusNotFound {
		resp.Diagnostics.AddError("Could not delete the rule", client.ProblemFrom(res.HTTPResponse, res.Body).Error())
	}
}

func fillRule(m *ruleModel, r *client.RuleBody) {
	m.ID = types.StringValue(r.Id)
	m.Name = types.StringValue(r.Name)
	m.EventTypes = fromStringSlice(r.EventTypes)
	m.ChannelIDs = fromStringSlice(r.ChannelIds)
	m.Enabled = types.BoolValue(r.Enabled)
	m.Filter = fromFilter(r.Filter, m.Filter)
}
