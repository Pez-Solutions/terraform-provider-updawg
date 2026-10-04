// Package provider is the Terraform provider: its configuration, and the
// resources and data sources it serves.
package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/Pez-Solutions/terraform-provider-updawg/internal/client"
)

// The environment a provider block may leave things to. A token is better
// there than in a .tf file, which tends to end up in git.
const (
	envURL   = "UPDAWG_API_URL"
	envToken = "UPDAWG_API_TOKEN"
	envOrg   = "UPDAWG_ORG"
)

// New returns the provider factory providerserver.Serve wants.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &updawgProvider{version: version}
	}
}

type updawgProvider struct {
	version string
}

type providerModel struct {
	APIURL   types.String `tfsdk:"api_url"`
	APIToken types.String `tfsdk:"api_token"`
	Org      types.String `tfsdk:"org"`
}

// providerData is what Configure hands every resource and data source.
type providerData struct {
	client *client.ClientWithResponses
	// org is the provider's default organization slug, or empty when the
	// configuration names one per resource instead.
	org string
}

func (p *updawgProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "updawg"
	resp.Version = p.version
}

func (p *updawgProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manage an [Updawg](https://updawg.net) organization's configuration: " +
			"groups, policies, enrollment tokens and notifications. Authenticates with an API " +
			"token (Business and Enterprise plans), which belongs to one organization.",
		Attributes: map[string]schema.Attribute{
			"api_url": schema.StringAttribute{
				MarkdownDescription: "The API to talk to. Defaults to `$" + envURL + "`, then `" + client.DefaultURL + "`.",
				Optional:            true,
			},
			"api_token": schema.StringAttribute{
				MarkdownDescription: "An API token (`upd_…`), issued under **Settings → Tokens → API tokens**. " +
					"Defaults to `$" + envToken + "`. It needs the scope for each kind of resource it manages: " +
					"`groups`, `policy`, `enrollment`, `integrations`, and `read` for data sources.",
				Optional:  true,
				Sensitive: true,
			},
			"org": schema.StringAttribute{
				MarkdownDescription: "The organization's slug, used by every resource that does not set its own `org`. " +
					"Defaults to `$" + envOrg + "`. To manage several organizations from one configuration, " +
					"use one provider block per organization with an `alias`, since a token belongs to one.",
				Optional: true,
			},
		},
	}
}

func (p *updawgProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// An unknown value — one that depends on another resource not yet
	// created — cannot configure a client. Say which, rather than failing
	// later on an empty string.
	for name, v := range map[string]types.String{"api_url": cfg.APIURL, "api_token": cfg.APIToken, "org": cfg.Org} {
		if v.IsUnknown() {
			resp.Diagnostics.AddAttributeError(path.Root(name), "Unknown provider setting",
				"The provider's "+name+" depends on a value not known until apply. Set it to a known value, or leave it to the environment.")
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}

	url := firstSet(cfg.APIURL.ValueString(), os.Getenv(envURL), client.DefaultURL)
	token := firstSet(cfg.APIToken.ValueString(), os.Getenv(envToken))
	org := firstSet(cfg.Org.ValueString(), os.Getenv(envOrg))

	if token == "" {
		resp.Diagnostics.AddAttributeError(path.Root("api_token"), "No API token",
			"Set api_token in the provider block or "+envToken+" in the environment. "+
				"Issue one under Settings → Tokens → API tokens in the Updawg portal.")
		return
	}

	c, err := client.New(url, token, "terraform-provider-updawg/"+p.version)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("api_url"), "Unusable API URL", err.Error())
		return
	}

	data := &providerData{client: c, org: org}
	resp.DataSourceData = data
	resp.ResourceData = data
}

func (p *updawgProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		newEnrollmentTokenResource,
		newGroupResource,
		newPolicyResource,
	}
}

func (p *updawgProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		newOrganizationDataSource,
	}
}

// firstSet returns the first non-empty value.
func firstSet(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
