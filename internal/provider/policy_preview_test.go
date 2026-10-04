package provider

import (
	"encoding/json"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/Pez-Solutions/terraform-provider-updawg/internal/client"
)

func TestPreviewSummary(t *testing.T) {
	var p client.PreviewBody
	err := json.Unmarshal([]byte(`{
  "hosts_evaluated": 500, "hosts_total": 1200, "complete": false, "would_auto_merge": 3, "changes_nothing": false,
  "opening": [
    {"dedupe_key": "a", "policy_id": "01a0fbad-0000-7000-8000-000000000001", "rule_index": 0, "kind": "security", "action": "auto_merge", "settings": {}, "hosts": 40, "packages": 3, "max_severity": "high"},
    {"dedupe_key": "b", "policy_id": "01a0fbad-0000-7000-8000-000000000001", "rule_index": 1, "kind": "patch", "action": "propose", "settings": {}, "hosts": 7, "packages": 12},
    {"dedupe_key": "c", "policy_id": "01a0fbad-0000-7000-8000-000000000001", "rule_index": 2, "kind": "kernel", "action": "propose", "settings": {}, "hosts": 2, "packages": 1, "same_work_as": "z"}
  ],
  "closing": [
    {"dedupe_key": "z", "policy_id": "01a0fbad-0000-7000-8000-000000000001", "rule_index": 3, "kind": "kernel", "action": "propose", "settings": {}, "hosts": 2, "packages": 1, "same_work_as": "c"}
  ],
  "changing": [
    {"dedupe_key": "d", "policy_id": "01a0fbad-0000-7000-8000-000000000001", "rule_index": 0, "kind": "security", "action": "auto_merge", "settings": {}, "hosts": 9, "packages": 2, "max_severity": "critical"}
  ],
  "unchanged": [],
  "dropped": [{"package": "openssl", "hosts": 12}, {"package": "curl", "hosts": 30}],
  "newly_covered": [{"package": "zlib", "hosts": 1}]
}`), &p)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := previewSummary(&p)
	want := `Saving this policy would, across the first 500 of 1200 hosts (counts are lower bounds):

⚠️ Stop deciding 2 updates nothing else covers: curl (30 hosts), openssl (12 hosts)

Open 2 proposals, 1 of which would auto-merge: security on 40 hosts (high) [auto-merge], patch on 7 hosts

Change 1 proposal, which would auto-merge: security on 9 hosts (critical) [auto-merge]

Move 1 proposal to another rule, the same work under a new key.

Start deciding 1 update nothing decides now: zlib (1 host)

Afterwards, 3 proposals would go out with nobody looking.`
	if !ok || got != want {
		t.Errorf("got:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestPreviewOfNothingSaysNothing(t *testing.T) {
	if _, ok := previewSummary(&client.PreviewBody{ChangesNothing: true}); ok {
		t.Error("a preview that changes nothing produced a warning")
	}
}

func TestPreviewListsAtMostFive(t *testing.T) {
	var cs []client.Coverage
	for i := range 8 {
		cs = append(cs, client.Coverage{Package: fmt.Sprintf("p%d", i), Hosts: 10 - i})
	}
	if got := coverage(cs); got != "p0 (10 hosts), p1 (9 hosts), p2 (8 hosts), p3 (7 hosts), p4 (6 hosts) and 3 more" {
		t.Errorf("got %q", got)
	}
}

func TestPolicyPlanAsksForAPreviewOfTheReplacement(t *testing.T) {
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
				Check: func(*terraform.State) error {
					if !f.sent(`POST /v1/orgs/acme/policies/preview \{"yaml":"name: nightly`) {
						return fmt.Errorf("no preview of the new policy: %q", f.requests)
					}
					return nil
				},
			},
			{
				// An edit is previewed as replacing the policy it edits, or
				// the preview would count the old and new versions both.
				Config: cfg(policyV2),
				Check: func(*terraform.State) error {
					if !f.sent(`POST /v1/orgs/acme/policies/preview \{"replaces":"pol_\d+","yaml":"name: nightly security\\npriority: 20`) {
						return fmt.Errorf("the edit was not previewed as a replacement: %q", f.requests)
					}
					return nil
				},
			},
		},
	})
}

func TestPolicyWithATakenNameFailsThePlan(t *testing.T) {
	f, srv := newFake(t)
	f.policies["pol_existing"] = &fakePolicy{yaml: policyV1, name: "nightly security", version: 1}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config: providerBlock(srv.URL, `org = "acme"`) + fmt.Sprintf(`
resource "updawg_policy" "copy" {
  yaml = <<-EOT
%sEOT
}
`, policyV1),
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`The policy's name is taken`),
		}},
	})
}
