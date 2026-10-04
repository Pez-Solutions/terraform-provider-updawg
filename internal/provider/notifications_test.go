package provider

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestChannelAndRuleLifecycle(t *testing.T) {
	f, srv := newFake(t)
	cfg := func(channelName, filter string) string {
		return providerBlock(srv.URL, `org = "acme"`) + fmt.Sprintf(`
resource "updawg_notification_channel" "ops" {
  name   = %q
  kind   = "slack"
  config = jsonencode({ webhook_url = "https://hooks.slack.com/services/T0/B0/WXYZ" })
}

resource "updawg_notification_rule" "security" {
  name        = "security to ops"
  event_types = ["proposal.opened", "proposal.auto_merged"]
  channel_ids = [updawg_notification_channel.ops.id]
  %s
}
`, channelName, filter)
	}
	const filter = `filter = {
    min_severity   = "high"
    proposal_kinds = ["security"]
  }`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: cfg("ops", filter),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("updawg_notification_channel.ops", "id", regexp.MustCompile(`^nch_`)),
					resource.TestCheckResourceAttr("updawg_notification_channel.ops", "display", "slack …WXYZ"),
					resource.TestCheckNoResourceAttr("updawg_notification_channel.ops", "signing_secret"),
					resource.TestCheckResourceAttr("updawg_notification_rule.security", "enabled", "true"),
					resource.TestCheckResourceAttr("updawg_notification_rule.security", "filter.min_severity", "high"),
					resource.TestCheckNoResourceAttr("updawg_notification_rule.security", "filter.known_exploited"),
					resource.TestCheckResourceAttrPair("updawg_notification_rule.security", "channel_ids.0", "updawg_notification_channel.ops", "id"),
				),
			},
			{
				ResourceName:      "updawg_notification_channel.ops",
				ImportState:       true,
				ImportStateVerify: true,
				// Never returned by the API, so an import cannot have it.
				ImportStateVerifyIgnore: []string{"config"},
			},
			{
				ResourceName:      "updawg_notification_rule.security",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// A rename alone must not resend the configuration; the
				// filter removed must be cleared, not left alone.
				Config: cfg("operations", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("updawg_notification_channel.ops", "name", "operations"),
					resource.TestCheckNoResourceAttr("updawg_notification_rule.security", "filter"),
					func(*terraform.State) error {
						if f.sent(`PATCH /v1/orgs/acme/notification-channels/\S+ .*"config"`) {
							return fmt.Errorf("a rename resent the configuration")
						}
						if !f.sent(`PATCH /v1/orgs/acme/notification-rules/\S+ .*"filter":\{\}`) {
							return fmt.Errorf("the filter was not cleared with {}: %q", f.requests)
						}
						return nil
					},
				),
			},
			{
				// Deleting the channel in the portal takes the rule with it.
				PreConfig: func() {
					f.mu.Lock()
					defer f.mu.Unlock()
					clear(f.channels)
					clear(f.rules)
				},
				Config:             cfg("operations", ""),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestWebhookWithAWriteOnlyConfiguration(t *testing.T) {
	f, srv := newFake(t)
	cfg := func(version int) string {
		return providerBlock(srv.URL, `org = "acme"`) + fmt.Sprintf(`
resource "updawg_notification_channel" "hook" {
  name              = "hook"
  kind              = "webhook"
  config_wo         = jsonencode({ url = "https://example.com/updawg" })
  config_wo_version = %d
}
`, version)
	}
	var first string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: cfg(1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("updawg_notification_channel.hook", "config_wo"),
					resource.TestCheckNoResourceAttr("updawg_notification_channel.hook", "config"),
					resource.TestCheckResourceAttrWith("updawg_notification_channel.hook", "signing_secret", func(v string) error {
						if !strings.HasPrefix(v, "whsec_") {
							return fmt.Errorf("signing_secret = %q", v)
						}
						first = v
						return nil
					}),
					func(*terraform.State) error {
						if !f.sent(`POST /v1/orgs/acme/notification-channels .*"config":\{"url":"https://example.com/updawg"\}`) {
							return fmt.Errorf("the write-only configuration was not sent: %q", f.requests)
						}
						return nil
					},
				),
			},
			{
				// A new version sends it again, and the API makes a new secret.
				Config: cfg(2),
				Check: resource.TestCheckResourceAttrWith("updawg_notification_channel.hook", "signing_secret", func(v string) error {
					if v == first || !strings.HasPrefix(v, "whsec_") {
						return fmt.Errorf("signing_secret %q after a new configuration (was %q)", v, first)
					}
					return nil
				}),
			},
		},
	})
}

func TestChannelConfigurationMistakesFailThePlan(t *testing.T) {
	_, srv := newFake(t)
	for name, tc := range map[string]struct{ body, want string }{
		"not JSON": {
			body: `config = "webhook_url=https://hooks.slack.com/x"`,
			want: `Not a JSON object`,
		},
		"both": {
			body: `config = jsonencode({ webhook_url = "x" })
  config_wo = jsonencode({ webhook_url = "x" })
  config_wo_version = 1`,
			want: `(?s)Invalid Attribute Combination`,
		},
		"neither": {
			body: ``,
			want: `Exactly one of these attributes must be configured`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: factories,
				Steps: []resource.TestStep{{
					Config: providerBlock(srv.URL, `org = "acme"`) + `
resource "updawg_notification_channel" "x" {
  name = "x"
  kind = "slack"
  ` + tc.body + `
}
`,
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tc.want),
				}},
			})
		})
	}
}
