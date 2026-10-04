package provider

import (
	"context"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pez-Solutions/terraform-provider-updawg/internal/client"
)

func newOrganizationDataSource() datasource.DataSource {
	return &organizationDataSource{}
}

type organizationDataSource struct {
	data *providerData
}

type organizationModel struct {
	Org               types.String `tfsdk:"org"`
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	Plan              types.String `tfsdk:"plan"`
	Role              types.String `tfsdk:"role"`
	StaleAfterMinutes types.Int64  `tfsdk:"stale_after_minutes"`
	AgentAutoUpdate   types.Bool   `tfsdk:"agent_auto_update"`
}

func (d *organizationDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization"
}

func (d *organizationDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The organization the token belongs to. Also the quickest check that a token and slug work together.",
		Attributes: map[string]schema.Attribute{
			"org": schema.StringAttribute{
				MarkdownDescription: orgAttribute,
				Optional:            true,
				Computed:            true,
			},
			"id": schema.StringAttribute{
				MarkdownDescription: "The organization's id.",
				Computed:            true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Its display name.",
				Computed:            true,
			},
			"plan": schema.StringAttribute{
				MarkdownDescription: "Its plan: `free`, `team`, `business` or `enterprise`.",
				Computed:            true,
			},
			"role": schema.StringAttribute{
				MarkdownDescription: "The role of the person who issued the token. A token can do at most what this role allows.",
				Computed:            true,
			},
			"stale_after_minutes": schema.Int64Attribute{
				MarkdownDescription: "Minutes without a check-in before a host is marked stale.",
				Computed:            true,
			},
			"agent_auto_update": schema.BoolAttribute{
				MarkdownDescription: "Whether agents are kept on the stable channel automatically.",
				Computed:            true,
			},
		},
	}
}

func (d *organizationDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	data, diags := configured(req.ProviderData)
	resp.Diagnostics.Append(diags...)
	d.data = data
}

func (d *organizationDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m organizationModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org, diags := resolveOrg(m.Org, d.data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	res, err := d.data.client.OrgsShowWithResponse(ctx, org)
	if err != nil {
		resp.Diagnostics.AddError("Could not read the organization", err.Error())
		return
	}
	if res.StatusCode() != http.StatusOK || res.JSON200 == nil {
		resp.Diagnostics.AddError("Could not read the organization", client.ProblemFrom(res.HTTPResponse, res.Body).Error())
		return
	}

	o := res.JSON200
	m.Org = types.StringValue(o.Slug)
	m.ID = types.StringValue(o.Id)
	m.Name = types.StringValue(o.Name)
	m.Plan = types.StringValue(o.Plan)
	m.Role = types.StringValue(string(o.Role))
	m.StaleAfterMinutes = types.Int64Value(int64(o.StaleAfterMinutes))
	m.AgentAutoUpdate = types.BoolValue(o.AgentAutoUpdate)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
