package provider

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestHostsAcrossPages(t *testing.T) {
	_, srv := newFake(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config: providerBlock(srv.URL, `org = "acme"`) + `
data "updawg_hosts" "all" {}

data "updawg_hosts" "web" {
  labels = { tier = "web" }
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				// Three hosts, two to a page.
				resource.TestCheckResourceAttr("data.updawg_hosts.all", "ids.#", "3"),
				resource.TestCheckResourceAttr("data.updawg_hosts.all", "ids.2", "hst_3"),
				resource.TestCheckResourceAttr("data.updawg_hosts.all", "hosts.0.hostname", "host-1"),
				resource.TestCheckResourceAttr("data.updawg_hosts.all", "hosts.0.reboot_required", "true"),
				resource.TestCheckResourceAttr("data.updawg_hosts.all", "hosts.2.labels.tier", "db"),
				resource.TestCheckNoResourceAttr("data.updawg_hosts.all", "hosts.0.display_name"),
				resource.TestCheckResourceAttr("data.updawg_hosts.web", "ids.#", "2"),
			),
		}},
	})
}

func TestGroupAndPolicyByName(t *testing.T) {
	f, srv := newFake(t)
	desc := "made in the portal"
	f.groups["grp_portal"] = &fakeGroup{name: "db", description: &desc, selector: map[string]string{"tier": "db"}}
	f.policies["pol_portal"] = &fakePolicy{yaml: policyV1, name: "nightly security", priority: 10, enabled: true, version: 3}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: providerBlock(srv.URL, `org = "acme"`) + `
data "updawg_group" "db" {
  name = "db"
}

data "updawg_policy" "nightly" {
  name = "nightly security"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.updawg_group.db", "id", "grp_portal"),
					resource.TestCheckResourceAttr("data.updawg_group.db", "description", desc),
					resource.TestCheckResourceAttr("data.updawg_group.db", "label_selector.tier", "db"),
					resource.TestCheckResourceAttr("data.updawg_group.db", "resolved_hosts", "1"),
					resource.TestCheckResourceAttr("data.updawg_policy.nightly", "id", "pol_portal"),
					resource.TestCheckResourceAttr("data.updawg_policy.nightly", "version", "3"),
					resource.TestCheckResourceAttr("data.updawg_policy.nightly", "yaml", policyV1),
				),
			},
			{
				Config:      providerBlock(srv.URL, `org = "acme"`) + `data "updawg_group" "x" { name = "nope" }`,
				ExpectError: regexp.MustCompile(`no group named nope`),
			},
		},
	})
}
