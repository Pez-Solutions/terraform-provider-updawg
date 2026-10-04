package provider

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/oapi-codegen/nullable"

	"github.com/Pez-Solutions/terraform-provider-updawg/internal/client"
)

var (
	_ resource.ResourceWithConfigure        = (*channelResource)(nil)
	_ resource.ResourceWithImportState      = (*channelResource)(nil)
	_ resource.ResourceWithModifyPlan       = (*channelResource)(nil)
	_ resource.ResourceWithConfigValidators = (*channelResource)(nil)
)

// channelKinds are what CreateChannelRequest.kind accepts.
var channelKinds = []string{"slack", "teams", "email", "ntfy", "pagerduty", "webhook"}

func newChannelResource() resource.Resource {
	return &channelResource{}
}

type channelResource struct {
	data *providerData
}

type channelModel struct {
	Org             types.String `tfsdk:"org"`
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	Kind            types.String `tfsdk:"kind"`
	Config          types.String `tfsdk:"config"`
	ConfigWO        types.String `tfsdk:"config_wo"`
	ConfigWOVersion types.Int64  `tfsdk:"config_wo_version"`
	Display         types.String `tfsdk:"display"`
	SigningSecret   types.String `tfsdk:"signing_secret"`
}

func (r *channelResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_notification_channel"
}

func (r *channelResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Somewhere notifications go: Slack, Teams, email, ntfy, PagerDuty or a signed webhook. " +
			"Rules (`updawg_notification_rule`) decide what is sent to it.\n\n" +
			"The API stores a channel's configuration encrypted and **never returns it**, so a change made in the portal " +
			"cannot be seen as drift; only the name and kind can. Give the configuration either as `config`, kept " +
			"(sensitive) in state, or as `config_wo` with `config_wo_version`, which Terraform 1.11+ and OpenTofu 1.11+ " +
			"never store: bump the version to send a new one. Slack, Teams, ntfy and PagerDuty need the Team plan, " +
			"webhooks Business. Needs the token scope `integrations`.",
		Attributes: map[string]schema.Attribute{
			"org": orgResourceAttribute(),
			"id":  idAttribute("The channel's id, `nch_…`."),
			"name": schema.StringAttribute{
				MarkdownDescription: "How rules and people pick it. Unique in the organization.",
				Required:            true,
			},
			"kind": schema.StringAttribute{
				MarkdownDescription: "`slack`, `teams`, `email`, `ntfy`, `pagerduty` or `webhook`. Changing it replaces the channel.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.OneOf(channelKinds...)},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"config": schema.StringAttribute{
				MarkdownDescription: "The kind's own fields as a JSON object, written with `jsonencode()`: `webhook_url` for " +
					"Slack and Teams; `to` (a list of addresses) for email; `topic_url` and optionally `token` for ntfy; " +
					"`routing_key` for PagerDuty; `url` for a webhook. Stored in state. Conflicts with `config_wo`.",
				Optional:   true,
				Sensitive:  true,
				Validators: []validator.String{jsonObject{}},
			},
			"config_wo": schema.StringAttribute{
				MarkdownDescription: "The same as `config`, never stored in state or plan. Sent when the channel is created " +
					"and whenever `config_wo_version` changes. Needs Terraform 1.11+ or OpenTofu 1.11+.",
				Optional:   true,
				Sensitive:  true,
				WriteOnly:  true,
				Validators: []validator.String{jsonObject{}},
			},
			"config_wo_version": schema.Int64Attribute{
				MarkdownDescription: "Change it to send `config_wo` again: Terraform cannot see a write-only value change.",
				Optional:            true,
			},
			"display": schema.StringAttribute{
				MarkdownDescription: "Enough of the configuration to tell channels apart (`hooks.slack.com …WXYZ`), never enough to use it.",
				Computed:            true,
			},
			"signing_secret": schema.StringAttribute{
				MarkdownDescription: "For a webhook, the `whsec_…` secret its requests are signed with. The API shows it " +
					"only when the configuration is set, so a new configuration makes a new secret, and an imported " +
					"channel has none.",
				Computed:  true,
				Sensitive: true,
			},
		},
	}
}

func (r *channelResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.ExactlyOneOf(path.MatchRoot("config"), path.MatchRoot("config_wo")),
		resourcevalidator.RequiredTogether(path.MatchRoot("config_wo"), path.MatchRoot("config_wo_version")),
		resourcevalidator.Conflicting(path.MatchRoot("config"), path.MatchRoot("config_wo_version")),
	}
}

func (r *channelResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	data, diags := configured(req.ProviderData)
	resp.Diagnostics.Append(diags...)
	r.data = data
}

func (r *channelResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importOrgAndID(ctx, req, resp)
}

// configChanged says whether an update sends a configuration. Not sending
// one when nothing changed matters: replacing a webhook's configuration
// makes it a new signing secret, and whoever verifies its requests would
// have to be told.
func configChanged(plan, state channelModel) bool {
	return !plan.Config.Equal(state.Config) || !plan.ConfigWOVersion.Equal(state.ConfigWOVersion)
}

// ModifyPlan keeps display and signing_secret as they are unless the
// configuration is being replaced, when the API will give new ones.
func (r *channelResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var plan, state channelModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || !plan.Kind.Equal(state.Kind) {
		return
	}
	if configChanged(plan, state) {
		if plan.Kind.ValueString() == "webhook" {
			resp.Diagnostics.AddAttributeWarning(path.Root("signing_secret"), "A new signing secret",
				"Replacing a webhook's configuration makes the API issue a new signing secret. Whatever verifies its requests needs the new one.")
		}
		return
	}
	plan.Display = state.Display
	plan.SigningSecret = state.SigningSecret
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

// channelConfig is the configuration to send: config from the plan, or the
// write-only config_wo, which only the configuration carries.
func channelConfig(ctx context.Context, plan channelModel, cfg tfConfig) (map[string]*client.JsonValue, diag.Diagnostics) {
	var diags diag.Diagnostics
	raw := plan.Config.ValueString()
	if plan.Config.IsNull() {
		var wo types.String
		diags.Append(cfg.GetAttribute(ctx, path.Root("config_wo"), &wo)...)
		if diags.HasError() {
			return nil, diags
		}
		raw = wo.ValueString()
	}
	out := map[string]*client.JsonValue{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		diags.AddAttributeError(path.Root("config"), "Unreadable configuration", err.Error())
	}
	return out, diags
}

// tfConfig is the part of tfsdk.Config channelConfig uses.
type tfConfig interface {
	GetAttribute(context.Context, path.Path, any) diag.Diagnostics
}

func (r *channelResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan channelModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	org, diags := resolveOrg(plan.Org, r.data)
	resp.Diagnostics.Append(diags...)
	config, diags := channelConfig(ctx, plan, req.Config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	res, err := r.data.client.NotificationChannelsCreateWithResponse(ctx, org, client.CreateChannelRequest{
		Name: plan.Name.ValueString(), Kind: plan.Kind.ValueString(), Config: config,
	})
	if err != nil {
		resp.Diagnostics.AddError("Could not create the channel", err.Error())
		return
	}
	if res.JSON201 == nil {
		resp.Diagnostics.AddError("Could not create the channel", client.ProblemFrom(res.HTTPResponse, res.Body).Error())
		return
	}
	plan.Org = types.StringValue(org)
	plan.ID = types.StringValue(res.JSON201.Id)
	fillSavedChannel(&plan, res.JSON201)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *channelResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state channelModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	org, diags := resolveOrg(state.Org, r.data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	res, err := r.data.client.NotificationChannelsListWithResponse(ctx, org)
	if err != nil {
		resp.Diagnostics.AddError("Could not read the channel", err.Error())
		return
	}
	if res.StatusCode() == http.StatusNotFound {
		resp.State.RemoveResource(ctx)
		return
	}
	if res.JSON200 == nil {
		resp.Diagnostics.AddError("Could not read the channel", client.ProblemFrom(res.HTTPResponse, res.Body).Error())
		return
	}
	for _, c := range *res.JSON200 {
		if c.Id == state.ID.ValueString() {
			state.Org = types.StringValue(org)
			state.Name = types.StringValue(c.Name)
			state.Kind = types.StringValue(c.Kind)
			state.Display = types.StringValue(c.Display)
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

func (r *channelResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state channelModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	org, diags := resolveOrg(plan.Org, r.data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := client.UpdateChannelRequest{Name: nullable.NewNullableWithValue(plan.Name.ValueString())}
	if configChanged(plan, state) {
		config, diags := channelConfig(ctx, plan, req.Config)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		body.Config = nullable.NewNullableWithValue(config)
	}
	res, err := r.data.client.NotificationChannelsUpdateWithResponse(ctx, org, state.ID.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError("Could not update the channel", err.Error())
		return
	}
	if res.JSON200 == nil {
		resp.Diagnostics.AddError("Could not update the channel", client.ProblemFrom(res.HTTPResponse, res.Body).Error())
		return
	}
	plan.ID = state.ID
	plan.Org = types.StringValue(org)
	plan.SigningSecret = state.SigningSecret
	fillSavedChannel(&plan, res.JSON200)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *channelResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state channelModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	org, diags := resolveOrg(state.Org, r.data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	res, err := r.data.client.NotificationChannelsDeleteWithResponse(ctx, org, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Could not delete the channel", err.Error())
		return
	}
	if res.StatusCode() != http.StatusNoContent && res.StatusCode() != http.StatusNotFound {
		resp.Diagnostics.AddError("Could not delete the channel", client.ProblemFrom(res.HTTPResponse, res.Body).Error())
	}
}

// fillSavedChannel takes what a save answers. The signing secret is only in
// the answer when the configuration was just set, so an answer without one
// leaves the one in state alone — except on creation, where there is none
// and null is right for every kind but a webhook.
func fillSavedChannel(m *channelModel, c *client.SavedChannelBody) {
	m.Name = types.StringValue(c.Name)
	m.Kind = types.StringValue(c.Kind)
	m.Display = types.StringValue(c.Display)
	if c.SigningSecret.IsSpecified() && !c.SigningSecret.IsNull() {
		m.SigningSecret = types.StringValue(c.SigningSecret.MustGet())
	} else if m.SigningSecret.IsUnknown() {
		m.SigningSecret = types.StringNull()
	}
}

// jsonObject validates that a string is a JSON object, at plan time, so
// that a typo is not a 400 halfway through apply.
type jsonObject struct{}

func (jsonObject) Description(context.Context) string { return "must be a JSON object" }

func (v jsonObject) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (jsonObject) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(req.ConfigValue.ValueString()), &obj); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Not a JSON object",
			"Write it with jsonencode({ ... }). "+err.Error())
	}
}
