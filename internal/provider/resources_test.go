package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestGroupLifecycle(t *testing.T) {
	f, srv := newFake(t)
	cfg := providerBlock(srv.URL, `org = "acme"`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: cfg + `
resource "updawg_group" "web" {
  name           = "web"
  description    = "The web tier"
  label_selector = { tier = "web" }
  hosts          = ["hst_3"]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("updawg_group.web", "org", "acme"),
					resource.TestMatchResourceAttr("updawg_group.web", "id", regexp.MustCompile(`^grp_`)),
					resource.TestCheckResourceAttr("updawg_group.web", "label_selector.tier", "web"),
					resource.TestCheckResourceAttr("updawg_group.web", "hosts.#", "1"),
					// hst_1 and hst_2 by label, hst_3 by hand.
					resource.TestCheckResourceAttr("updawg_group.web", "resolved_hosts", "3"),
				),
			},
			{
				ResourceName:      "updawg_group.web",
				ImportState:       true,
				ImportStateVerify: true,
				// Hand-picked membership is not managed until configured.
				ImportStateVerifyIgnore: []string{"hosts"},
			},
			{
				// The description and selector removed: both must be sent
				// as null, or the API leaves them alone.
				Config: cfg + `
resource "updawg_group" "web" {
  name  = "frontend"
  hosts = ["hst_1"]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("updawg_group.web", "name", "frontend"),
					resource.TestCheckNoResourceAttr("updawg_group.web", "description"),
					resource.TestCheckNoResourceAttr("updawg_group.web", "label_selector"),
					resource.TestCheckResourceAttr("updawg_group.web", "resolved_hosts", "1"),
					func(*terraform.State) error {
						if !f.sent(`PATCH /v1/orgs/acme/groups/grp_\d+ \{"description":null,"label_selector":null,"name":"frontend"\}`) {
							return fmt.Errorf("no PATCH clearing description and selector in %q", f.requests)
						}
						return nil
					},
				),
			},
			{
				// Hosts added in the portal are drift once hosts is managed.
				PreConfig: func() {
					f.mu.Lock()
					defer f.mu.Unlock()
					for _, g := range f.groups {
						g.static = append(g.static, "hst_2")
					}
				},
				Config: cfg + `
resource "updawg_group" "web" {
  name  = "frontend"
  hosts = ["hst_1"]
}
`,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestGroupDeletedInThePortalIsRecreated(t *testing.T) {
	f, srv := newFake(t)
	cfg := providerBlock(srv.URL, `org = "acme"`) + `resource "updawg_group" "db" { name = "db" }`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				PreConfig: func() {
					f.mu.Lock()
					defer f.mu.Unlock()
					clear(f.groups)
				},
				Config:             cfg,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

const policyV1 = `name: nightly security
priority: 10
rules:
  - match: security
    apply: asap
`

const policyV2 = `name: nightly security
priority: 20
enabled: false
rules:
  - match: security
    apply: asap
`

func TestPolicyLifecycle(t *testing.T) {
	f, srv := newFake(t)
	cfg := func(yaml string) string {
		return providerBlock(srv.URL, `org = "acme"`) + fmt.Sprintf(`
resource "updawg_policy" "nightly" {
  yaml = <<-EOT
%sEOT
}
`, yaml)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: cfg(policyV1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("updawg_policy.nightly", "id", regexp.MustCompile(`^pol_`)),
					resource.TestCheckResourceAttr("updawg_policy.nightly", "name", "nightly security"),
					resource.TestCheckResourceAttr("updawg_policy.nightly", "priority", "10"),
					resource.TestCheckResourceAttr("updawg_policy.nightly", "enabled", "true"),
					resource.TestCheckResourceAttr("updawg_policy.nightly", "version", "1"),
					resource.TestCheckResourceAttr("updawg_policy.nightly", "yaml", policyV1),
				),
			},
			{
				ResourceName:      "updawg_policy.nightly",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// The plan already knows what the document says.
				Config: cfg(policyV2),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("updawg_policy.nightly", "priority", "20"),
					resource.TestCheckResourceAttr("updawg_policy.nightly", "enabled", "false"),
					resource.TestCheckResourceAttr("updawg_policy.nightly", "version", "2"),
				),
			},
			{
				// Edited in the portal: the next plan puts the text back.
				PreConfig: func() {
					f.mu.Lock()
					defer f.mu.Unlock()
					for _, p := range f.policies {
						p.yaml += "# tweaked in the portal\n"
						p.version++
					}
				},
				Config:             cfg(policyV2),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: cfg(policyV2),
				Check:  resource.TestCheckResourceAttr("updawg_policy.nightly", "version", "4"),
			},
		},
	})
}

func TestPolicyThatDoesNotCompileFailsThePlan(t *testing.T) {
	f, srv := newFake(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config: providerBlock(srv.URL, `org = "acme"`) + `
resource "updawg_policy" "broken" {
  yaml = "name: broken\nbogus\n"
}
`,
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`line 2, column 1: unknown field ` + "`bogus`"),
		}},
	})
	if f.sent(`^POST /v1/orgs/acme/policies \{`) {
		t.Error("a policy that does not compile was sent to be saved")
	}
}

func TestEnrollmentTokenLifecycle(t *testing.T) {
	f, srv := newFake(t)
	cfg := func(name string) string {
		return providerBlock(srv.URL, `org = "acme"`) + fmt.Sprintf(`
resource "updawg_enrollment_token" "web" {
  name       = %q
  labels     = { tier = "web" }
  max_uses   = 10
  expires_at = "2027-01-01T00:00:00Z"
}
`, name)
	}
	var firstID string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				// The fake answers expires_at as 2027-01-01T00:00:00.000000+00:00;
				// the post-apply plan being empty is the check that this
				// is not a change.
				Config: cfg("web tier"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("updawg_enrollment_token.web", "id", regexp.MustCompile(`^etk_`)),
					resource.TestMatchResourceAttr("updawg_enrollment_token.web", "token", regexp.MustCompile(`^enr_secret_etk_`)),
					resource.TestCheckResourceAttr("updawg_enrollment_token.web", "expires_at", "2027-01-01T00:00:00Z"),
					resource.TestCheckResourceAttr("updawg_enrollment_token.web", "usable", "true"),
					resource.TestCheckResourceAttrWith("updawg_enrollment_token.web", "id", func(v string) error { firstID = v; return nil }),
				),
			},
			{
				ResourceName:      "updawg_enrollment_token.web",
				ImportState:       true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return "acme/" + firstID, nil },
				ImportStateVerify: true,
				// The value is shown once; an import cannot have it, and
				// the API's spelling of the instant is what an import sees.
				ImportStateVerifyIgnore: []string{"token", "expires_at"},
			},
			{
				// No PATCH: a new name is a new token, and the old revoked.
				Config: cfg("web servers"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith("updawg_enrollment_token.web", "id", func(v string) error {
						if v == firstID {
							return fmt.Errorf("still %s after a rename", v)
						}
						return nil
					}),
					func(*terraform.State) error {
						if !f.sent(`DELETE /v1/orgs/acme/enrollment-tokens/` + firstID) {
							return fmt.Errorf("the old token was not revoked")
						}
						return nil
					},
				),
			},
			{
				// Revoked in the portal: planned for re-issue.
				PreConfig: func() {
					f.mu.Lock()
					defer f.mu.Unlock()
					for _, tok := range f.tokens {
						tok.revoked = true
					}
				},
				Config:             cfg("web servers"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestEnrollmentTokenWithNoLabels(t *testing.T) {
	_, srv := newFake(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			// The API answers labels {} for none; that is not a change from
			// a configuration that leaves them out.
			Config: providerBlock(srv.URL, `org = "acme"`) + `resource "updawg_enrollment_token" "bare" { name = "bare" }`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckNoResourceAttr("updawg_enrollment_token.bare", "labels"),
				resource.TestCheckNoResourceAttr("updawg_enrollment_token.bare", "expires_at"),
			),
		}},
	})
}
