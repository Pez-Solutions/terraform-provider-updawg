package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// factories serves the provider in-process to whichever binary the testing
// framework runs: terraform, or tofu with TF_ACC_TERRAFORM_PATH pointing at
// it.
var factories = map[string]func() (tfprotov6.ProviderServer, error){
	"updawg": providerserver.NewProtocol6WithError(New("test")()),
}

const testToken = "upd_test"

// fakeAPI answers like the API for the organizations it is given, checking
// the token on every request.
func fakeAPI(t *testing.T, orgs map[string]map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			problem(w, 401, "unauthenticated", "Not signed in")
			return
		}
		if r.Method == http.MethodGet && len(r.URL.Path) > len("/v1/orgs/") {
			if org, ok := orgs[r.URL.Path[len("/v1/orgs/"):]]; ok {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(org)
				return
			}
		}
		problem(w, 404, "not-found", "Not found")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func problem(w http.ResponseWriter, status int, kind, title string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type": "https://updawg.net/problems/" + kind, "title": title, "status": status,
	})
}

func org(slug, name string) map[string]any {
	return map[string]any{
		"id": "01a0fbad-0000-7000-8000-" + slug, "slug": slug, "name": name, "plan": "business",
		"role": "admin", "stale_after_minutes": 30, "eol_lead_days": []int{180, 90, 30},
		"audit_retention_max_days": 365, "agent_auto_update": false,
	}
}

func providerBlock(url, extra string) string {
	return `provider "updawg" {
  api_url   = "` + url + `"
  api_token = "` + testToken + `"
  ` + extra + `
}
`
}

func TestOrganizationFromTheProviderAndOverridden(t *testing.T) {
	srv := fakeAPI(t, map[string]map[string]any{
		"acme":   org("acme", "Acme"),
		"globex": org("globex", "Globex"),
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config: providerBlock(srv.URL, `org = "acme"`) + `
data "updawg_organization" "default" {}
data "updawg_organization" "other" {
  org = "globex"
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.updawg_organization.default", "org", "acme"),
				resource.TestCheckResourceAttr("data.updawg_organization.default", "name", "Acme"),
				resource.TestCheckResourceAttr("data.updawg_organization.default", "plan", "business"),
				resource.TestCheckResourceAttr("data.updawg_organization.default", "stale_after_minutes", "30"),
				resource.TestCheckResourceAttr("data.updawg_organization.other", "name", "Globex"),
			),
		}},
	})
}

func TestTheOrganizationFromTheEnvironment(t *testing.T) {
	srv := fakeAPI(t, map[string]map[string]any{"acme": org("acme", "Acme")})
	t.Setenv(envOrg, "acme")
	t.Setenv(envToken, testToken)
	t.Setenv(envURL, srv.URL)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config: `data "updawg_organization" "this" {}`,
			Check:  resource.TestCheckResourceAttr("data.updawg_organization.this", "name", "Acme"),
		}},
	})
}

func TestNoOrganizationAnywhere(t *testing.T) {
	srv := fakeAPI(t, nil)
	t.Setenv(envOrg, "")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config:      providerBlock(srv.URL, "") + `data "updawg_organization" "this" {}`,
			ExpectError: regexp.MustCompile(`No organization`),
		}},
	})
}

func TestNoToken(t *testing.T) {
	t.Setenv(envToken, "")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config:      `provider "updawg" { org = "acme" }` + "\n" + `data "updawg_organization" "this" {}`,
			ExpectError: regexp.MustCompile(`No API token`),
		}},
	})
}

func TestAnOrganizationTheTokenCannotSee(t *testing.T) {
	srv := fakeAPI(t, map[string]map[string]any{"acme": org("acme", "Acme")})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config:      providerBlock(srv.URL, `org = "initech"`) + `data "updawg_organization" "this" {}`,
			ExpectError: regexp.MustCompile(`404 Not found \(not-found\)`),
		}},
	})
}
