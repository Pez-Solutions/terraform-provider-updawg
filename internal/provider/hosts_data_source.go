package provider

import (
	"context"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pez-Solutions/terraform-provider-updawg/internal/client"
)

// maxHostPages bounds how many pages one read follows: 50 hosts a page, so
// 20,000 hosts. A fleet past that wants a narrower filter, not a slower plan.
const maxHostPages = 400

var hostAttrTypes = map[string]attr.Type{
	"id":              types.StringType,
	"hostname":        types.StringType,
	"display_name":    types.StringType,
	"distro":          types.StringType,
	"distro_version":  types.StringType,
	"arch":            types.StringType,
	"status":          types.StringType,
	"labels":          types.MapType{ElemType: types.StringType},
	"agent_version":   types.StringType,
	"reboot_required": types.BoolType,
}

func newHostsDataSource() datasource.DataSource {
	return &hostsDataSource{}
}

type hostsDataSource struct {
	data *providerData
}

type hostsModel struct {
	Org            types.String `tfsdk:"org"`
	Labels         types.Map    `tfsdk:"labels"`
	Statuses       types.Set    `tfsdk:"statuses"`
	Distros        types.Set    `tfsdk:"distros"`
	RebootRequired types.Set    `tfsdk:"reboot_required"`
	IDs            types.List   `tfsdk:"ids"`
	Hosts          types.List   `tfsdk:"hosts"`
}

func (d *hostsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_hosts"
}

func (d *hostsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The organization's hosts, filtered, by hostname. For putting existing hosts into an " +
			"`updawg_group` by hand, or anything else that should follow the fleet as it is. Needs the token scope `read`.",
		Attributes: map[string]schema.Attribute{
			"org": schema.StringAttribute{MarkdownDescription: orgAttribute, Optional: true, Computed: true},
			"labels": schema.MapAttribute{
				MarkdownDescription: "Only hosts carrying **all** of these labels with these values.",
				ElementType:         types.StringType,
				Optional:            true,
			},
			"statuses": schema.SetAttribute{
				MarkdownDescription: "Only hosts in one of these states, such as `active` or `stale`.",
				ElementType:         types.StringType,
				Optional:            true,
			},
			"distros": schema.SetAttribute{
				MarkdownDescription: "Only hosts running one of these distributions, such as `ubuntu` or `rocky`.",
				ElementType:         types.StringType,
				Optional:            true,
			},
			"reboot_required": schema.SetAttribute{
				MarkdownDescription: "Only hosts whose reboot state is one of `yes`, `no` or `unknown`.",
				ElementType:         types.StringType,
				Optional:            true,
				Validators:          []validator.Set{setvalidator.ValueStringsAre(stringvalidator.OneOf("yes", "no", "unknown"))},
			},
			"ids": schema.ListAttribute{
				MarkdownDescription: "The matching hosts' `hst_…` ids, in hostname order.",
				ElementType:         types.StringType,
				Computed:            true,
			},
			"hosts": schema.ListNestedAttribute{
				MarkdownDescription: "The matching hosts, in hostname order.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":              schema.StringAttribute{Computed: true, MarkdownDescription: "`hst_…`."},
					"hostname":        schema.StringAttribute{Computed: true, MarkdownDescription: "As the host reports it."},
					"display_name":    schema.StringAttribute{Computed: true, MarkdownDescription: "The name given in the portal, if any."},
					"distro":          schema.StringAttribute{Computed: true, MarkdownDescription: "The distribution."},
					"distro_version":  schema.StringAttribute{Computed: true, MarkdownDescription: "Its version."},
					"arch":            schema.StringAttribute{Computed: true, MarkdownDescription: "The architecture."},
					"status":          schema.StringAttribute{Computed: true, MarkdownDescription: "The host's state."},
					"labels":          schema.MapAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Its labels."},
					"agent_version":   schema.StringAttribute{Computed: true, MarkdownDescription: "The agent it runs, once it has said."},
					"reboot_required": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether it needs a reboot; null when it cannot tell."},
				}},
			},
		},
	}
}

func (d *hostsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	data, diags := configured(req.ProviderData)
	resp.Diagnostics.Append(diags...)
	d.data = data
}

func (d *hostsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m hostsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	org, diags := resolveOrg(m.Org, d.data)
	resp.Diagnostics.Append(diags...)
	labels, diags := toStringMap(ctx, m.Labels)
	resp.Diagnostics.Append(diags...)
	var params client.HostsListParams
	for _, f := range []struct {
		set  types.Set
		into **[]string
	}{{m.Statuses, &params.Status}, {m.Distros, &params.Distro}, {m.RebootRequired, &params.RebootRequired}} {
		if !f.set.IsNull() {
			values, d := toStringSlice(ctx, f.set)
			resp.Diagnostics.Append(d...)
			*f.into = &values
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if len(labels) > 0 {
		keys := make([]string, 0, len(labels))
		for k := range labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		selector := make([]string, 0, len(keys))
		for _, k := range keys {
			selector = append(selector, k+"="+labels[k])
		}
		params.Label = &selector
	}

	var hosts []client.HostBody
	for page := 0; ; page++ {
		if page == maxHostPages {
			resp.Diagnostics.AddError("Too many hosts", "More than 20,000 hosts match. Narrow the filter.")
			return
		}
		res, err := d.data.client.HostsListWithResponse(ctx, org, &params)
		if err != nil {
			resp.Diagnostics.AddError("Could not list hosts", err.Error())
			return
		}
		if res.JSON200 == nil {
			resp.Diagnostics.AddError("Could not list hosts", client.ProblemFrom(res.HTTPResponse, res.Body).Error())
			return
		}
		hosts = append(hosts, res.JSON200.Hosts...)
		next := res.JSON200.Next
		if !next.IsSpecified() || next.IsNull() || next.MustGet() == "" {
			break
		}
		after := next.MustGet()
		params.After = &after
	}

	ids := make([]attr.Value, len(hosts))
	objs := make([]attr.Value, len(hosts))
	for i, h := range hosts {
		ids[i] = types.StringValue(h.Id)
		reboot := types.BoolNull()
		if h.RebootRequired.IsSpecified() && !h.RebootRequired.IsNull() {
			reboot = types.BoolValue(h.RebootRequired.MustGet())
		}
		objs[i] = types.ObjectValueMust(hostAttrTypes, map[string]attr.Value{
			"id":              types.StringValue(h.Id),
			"hostname":        types.StringValue(h.Hostname),
			"display_name":    fromNullableString(h.DisplayName),
			"distro":          types.StringValue(h.Distro),
			"distro_version":  types.StringValue(h.DistroVersion),
			"arch":            types.StringValue(h.Arch),
			"status":          types.StringValue(h.Status),
			"labels":          fromStringMap(nonNil(h.Labels)),
			"agent_version":   fromNullableString(h.AgentVersion),
			"reboot_required": reboot,
		})
	}
	m.Org = types.StringValue(org)
	m.IDs = types.ListValueMust(types.StringType, ids)
	m.Hosts = types.ListValueMust(types.ObjectType{AttrTypes: hostAttrTypes}, objs)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
