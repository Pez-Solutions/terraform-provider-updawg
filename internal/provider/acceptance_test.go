package provider

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAcceptance runs every resource against a real organization
// (DAWG-139): the only test that proves the provider and the API agree.
// The fake-API tests prove the provider drives Terraform correctly; this one
// proves the wire format is what the spec says and the server does what its
// descriptions promise.
//
// It runs with TF_ACC=1 and UPDAWG_API_TOKEN and UPDAWG_ORG set, for a
// dedicated test organization on the Business plan (webhooks, API tokens)
// whose token has the scopes read, groups, policy, enrollment and
// integrations. Everything it makes carries a random suffix and is
// destroyed at the end. The policy is disabled and selects a group no host
// is in, so it cannot propose anything on a real host.
func TestAcceptance(t *testing.T) {
	if os.Getenv("UPDAWG_ORG") == "" || os.Getenv("UPDAWG_API_TOKEN") == "" {
		t.Skip("UPDAWG_ORG and UPDAWG_API_TOKEN name the test organization; not set")
	}
	suffix := acctest.RandString(8)
	cfg := func(description string, priority int) string {
		return fmt.Sprintf(`
data "updawg_organization" "this" {}

resource "updawg_group" "acc" {
  name           = "tf-acc-%[1]s"
  description    = %[2]q
  label_selector = { tf-acc = "%[1]s" }
}

resource "updawg_policy" "acc" {
  yaml = <<-EOT
    name: tf-acc-%[1]s
    priority: %[3]d
    enabled: false
    selector:
      groups: [${updawg_group.acc.name}]
    rules:
      - action: propose
  EOT
}

resource "updawg_enrollment_token" "acc" {
  name       = "tf-acc-%[1]s"
  labels     = { tf-acc = "%[1]s" }
  max_uses   = 1
  expires_at = "2030-01-01T00:00:00Z"
}

resource "updawg_notification_channel" "acc" {
  name   = "tf-acc-%[1]s"
  kind   = "webhook"
  config = jsonencode({ url = "https://example.com/updawg-tf-acc" })
}

resource "updawg_notification_rule" "acc" {
  name        = "tf-acc-%[1]s"
  event_types = ["host.stale"]
  channel_ids = [updawg_notification_channel.acc.id]
  enabled     = false
}
`, suffix, description, priority)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: cfg("made by the provider's acceptance test", 900),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.updawg_organization.this", "plan"),
					resource.TestCheckResourceAttr("updawg_policy.acc", "enabled", "false"),
					resource.TestCheckResourceAttr("updawg_policy.acc", "version", "1"),
					resource.TestCheckResourceAttrSet("updawg_enrollment_token.acc", "token"),
					resource.TestCheckResourceAttr("updawg_enrollment_token.acc", "usable", "true"),
					resource.TestCheckResourceAttrSet("updawg_notification_channel.acc", "signing_secret"),
					resource.TestCheckResourceAttrSet("updawg_notification_channel.acc", "display"),
				),
			},
			{ResourceName: "updawg_group.acc", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"hosts"}},
			{ResourceName: "updawg_policy.acc", ImportState: true, ImportStateVerify: true},
			{ResourceName: "updawg_enrollment_token.acc", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"token", "expires_at"}},
			{ResourceName: "updawg_notification_channel.acc", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"config", "signing_secret"}},
			{ResourceName: "updawg_notification_rule.acc", ImportState: true, ImportStateVerify: true},
			{
				Config: cfg("changed by the provider's acceptance test", 901),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("updawg_group.acc", "description", "changed by the provider's acceptance test"),
					resource.TestCheckResourceAttr("updawg_policy.acc", "priority", "901"),
					resource.TestCheckResourceAttr("updawg_policy.acc", "version", "2"),
				),
			},
		},
	})
}
