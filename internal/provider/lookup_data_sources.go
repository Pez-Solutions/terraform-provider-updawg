package provider

import (
	"context"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pez-Solutions/terraform-provider-updawg/internal/client"
)

// updawg_group and updawg_policy find something made elsewhere — in the
// portal, or by another configuration — by the name people know it by.

func newGroupDataSource() datasource.DataSource {
	return &groupDataSource{}
}

type groupDataSource struct {
	data *providerData
}

type groupLookupModel struct {
	Org           types.String `tfsdk:"org"`
	Name          types.String `tfsdk:"name"`
	ID            types.String `tfsdk:"id"`
	Description   types.String `tfsdk:"description"`
	LabelSelector types.Map    `tfsdk:"label_selector"`
	ResolvedHosts types.Int64  `tfsdk:"resolved_hosts"`
}

func (d *groupDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group"
}

func (d *groupDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A host group, by name. Needs the token scope `read`.",
		Attributes: map[string]schema.Attribute{
			"org":            schema.StringAttribute{MarkdownDescription: orgAttribute, Optional: true, Computed: true},
			"name":           schema.StringAttribute{MarkdownDescription: "The group's name.", Required: true},
			"id":             schema.StringAttribute{MarkdownDescription: "`grp_…`.", Computed: true},
			"description":    schema.StringAttribute{MarkdownDescription: "What it is for.", Computed: true},
			"label_selector": schema.MapAttribute{MarkdownDescription: "The labels that put a host in it.", ElementType: types.StringType, Computed: true},
			"resolved_hosts": schema.Int64Attribute{MarkdownDescription: "How many hosts it holds now.", Computed: true},
		},
	}
}

func (d *groupDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	data, diags := configured(req.ProviderData)
	resp.Diagnostics.Append(diags...)
	d.data = data
}

func (d *groupDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m groupLookupModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	org, diags := resolveOrg(m.Org, d.data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, err := d.data.client.GroupsListWithResponse(ctx, org)
	if err != nil {
		resp.Diagnostics.AddError("Could not list groups", err.Error())
		return
	}
	if res.JSON200 == nil {
		resp.Diagnostics.AddError("Could not list groups", client.ProblemFrom(res.HTTPResponse, res.Body).Error())
		return
	}
	for _, g := range *res.JSON200 {
		if g.Name != m.Name.ValueString() {
			continue
		}
		m.Org = types.StringValue(org)
		m.ID = types.StringValue(g.Id)
		m.Description = fromNullableString(g.Description)
		m.LabelSelector = types.MapNull(types.StringType)
		if g.LabelSelector.IsSpecified() && !g.LabelSelector.IsNull() {
			m.LabelSelector = fromStringMap(g.LabelSelector.MustGet())
		}
		m.ResolvedHosts = types.Int64Value(g.ResolvedHosts)
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		return
	}
	resp.Diagnostics.AddError("No such group", "The organization has no group named "+m.Name.ValueString()+".")
}

func newPolicyDataSource() datasource.DataSource {
	return &policyDataSource{}
}

type policyDataSource struct {
	data *providerData
}

type policyLookupModel struct {
	Org      types.String `tfsdk:"org"`
	Name     types.String `tfsdk:"name"`
	ID       types.String `tfsdk:"id"`
	YAML     types.String `tfsdk:"yaml"`
	Priority types.Int64  `tfsdk:"priority"`
	Enabled  types.Bool   `tfsdk:"enabled"`
	Version  types.Int64  `tfsdk:"version"`
}

func (d *policyDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_policy"
}

func (d *policyDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A live policy, by name, with the document in force. Needs the token scope `read`.",
		Attributes: map[string]schema.Attribute{
			"org":      schema.StringAttribute{MarkdownDescription: orgAttribute, Optional: true, Computed: true},
			"name":     schema.StringAttribute{MarkdownDescription: "The policy's name.", Required: true},
			"id":       schema.StringAttribute{MarkdownDescription: "`pol_…`.", Computed: true},
			"yaml":     schema.StringAttribute{MarkdownDescription: "The document in force.", Computed: true},
			"priority": schema.Int64Attribute{MarkdownDescription: "Its priority.", Computed: true},
			"enabled":  schema.BoolAttribute{MarkdownDescription: "Whether it is enabled.", Computed: true},
			"version":  schema.Int64Attribute{MarkdownDescription: "The version in force.", Computed: true},
		},
	}
}

func (d *policyDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	data, diags := configured(req.ProviderData)
	resp.Diagnostics.Append(diags...)
	d.data = data
}

func (d *policyDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m policyLookupModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	org, diags := resolveOrg(m.Org, d.data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	list, err := d.data.client.PoliciesListWithResponse(ctx, org)
	if err != nil {
		resp.Diagnostics.AddError("Could not list policies", err.Error())
		return
	}
	if list.JSON200 == nil {
		resp.Diagnostics.AddError("Could not list policies", client.ProblemFrom(list.HTTPResponse, list.Body).Error())
		return
	}
	id := ""
	for _, p := range *list.JSON200 {
		if p.Name == m.Name.ValueString() {
			id = p.Id
		}
	}
	if id == "" {
		resp.Diagnostics.AddError("No such policy", "The organization has no live policy named "+m.Name.ValueString()+".")
		return
	}
	res, err := d.data.client.PoliciesShowWithResponse(ctx, org, id, nil)
	if err != nil {
		resp.Diagnostics.AddError("Could not read the policy", err.Error())
		return
	}
	if res.StatusCode() != http.StatusOK || res.JSON200 == nil {
		resp.Diagnostics.AddError("Could not read the policy", client.ProblemFrom(res.HTTPResponse, res.Body).Error())
		return
	}
	p := res.JSON200
	m.Org = types.StringValue(org)
	m.ID = types.StringValue(p.Id)
	m.YAML = types.StringValue(p.Yaml)
	m.Priority = types.Int64Value(int64(p.Priority))
	m.Enabled = types.BoolValue(p.Enabled)
	m.Version = types.Int64Value(int64(p.Version))
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
