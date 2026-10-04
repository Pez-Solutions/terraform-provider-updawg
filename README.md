# terraform-provider-updawg

> *What's Updawg? Not much. Your servers are up to date.*

A Terraform and OpenTofu provider for an Updawg organization's configuration:
groups, policies, enrollment tokens and notifications.

**Not `updawg-infra`.** That repository runs Updawg's own cloud. This one
manages *customers'* Updawg configuration.

**Status:** `updawg_group`, `updawg_policy`, `updawg_enrollment_token`
(DAWG-135), `updawg_notification_channel`, `updawg_notification_rule`
(DAWG-136), and the `updawg_organization` data source. Not published to either registry yet
(DAWG-139). See [PLAN.md](PLAN.md) for the design.

## Using it

```hcl
provider "updawg" {
  org = "acme" # or UPDAWG_ORG
  # api_token from UPDAWG_API_TOKEN; keep it out of .tf files
}

resource "updawg_group" "web" {
  name           = "web"
  label_selector = { tier = "web" }
}

resource "updawg_policy" "nightly" {
  yaml = file("${path.module}/policies/nightly.yaml")
}

resource "updawg_enrollment_token" "web" {
  name       = "web tier"
  labels     = { tier = "web" }
  max_uses   = 20
  expires_at = "2027-01-01T00:00:00Z"
}
```

| Setting     | Environment        | Default                  |
| ----------- | ------------------ | ------------------------ |
| `api_token` | `UPDAWG_API_TOKEN` | none: required           |
| `org`       | `UPDAWG_ORG`       | none: here or per block  |
| `api_url`   | `UPDAWG_API_URL`   | `https://api.updawg.net` |

The token is an API token (`upd_…`) from **Settings → Tokens → API tokens**
(Business and Enterprise). It acts as the person who issued it, within its
scopes: `groups`, `policy`, `enrollment` and `integrations` for the resources
of each kind, `read` for data sources.

Every resource and data source takes its own `org`, defaulting to the
provider's. A token belongs to one organization, so managing several means
one aliased provider block, and one token, per organization:

```hcl
provider "updawg" {
  alias     = "globex"
  org       = "globex"
  api_token = var.globex_token
}
```

## What to know about each resource

- **`updawg_policy`** takes the YAML document as it is, byte for byte. `plan`
  sends it to the API's validator, so a document that does not compile fails
  the plan with its line and column, and the plan shows the name, priority
  and enabled it will have. If the validator can't be reached, the plan
  carries on with a warning. An edit made in the portal shows as a change to
  `yaml`; applying puts your text back as a new version. Deleting is soft, so
  proposals the policy made keep their reason.
- **`updawg_group`** manages the name, description and label selector. Hand-picked
  members (`hosts`) are managed only when you set them. Policies select groups by
  name, so a rename or delete warns about every policy still naming the
  group.
- **`updawg_enrollment_token`** can't be changed in place (the API has no way
  to), so any change issues a new token and revokes the old one. One revoked in
  the portal is planned for re-issue. ⚠️ **The token's value is stored in
  state.** The API shows it once and keeps only a hash, so state is the only
  place Terraform can keep it to pass to anything else. Treat state as a
  secret, and use `max_uses` and `expires_at` to limit what a leaked value is
  worth. Whether to keep it this way is DAWG-137.

- **`updawg_notification_channel`**: the API stores the configuration
  encrypted and never returns it, so a change made in the portal can't show as
  drift. Give it as `config = jsonencode({...})` (sensitive, kept in state) or
  as `config_wo` plus `config_wo_version` (never stored; Terraform/OpenTofu
  1.11+; bump the version to resend). The configuration is only sent when it
  changes, because a new one gives a webhook a new `signing_secret`.
- **`updawg_notification_rule`**: deleting a channel deletes the rules that send
  to it, and the next plan recreates them.

```hcl
resource "updawg_notification_channel" "ops" {
  name              = "ops"
  kind              = "slack"
  config_wo         = jsonencode({ webhook_url = var.slack_webhook })
  config_wo_version = 1
}

resource "updawg_notification_rule" "security" {
  name        = "security to ops"
  event_types = ["proposal.opened", "proposal.auto_merged"]
  channel_ids = [updawg_notification_channel.ops.id]
  filter      = { min_severity = "high" }
}
```

Every resource imports with `org/id` or a bare `id`:

```sh
terraform import updawg_group.web acme/grp_…
```

## Working on it

```sh
make test        # the provider tests, with terraform on PATH
make test-tofu   # the same with OpenTofu
make lint
```

The tests serve the provider in-process to the real CLI and point it at a
stateful fake API (`internal/provider/fake_api_test.go`), which answers in
the spec's wire format and records what it was sent. They prove the provider
drives Terraform correctly, not that it agrees with the real API: that is the
acceptance tests' job (DAWG-139).

### Acceptance tests

`TestAcceptance` runs every resource against a real organization: the only
test that proves the provider and the API agree. It skips unless `TF_ACC=1`,
`UPDAWG_ORG` and `UPDAWG_API_TOKEN` are set. Use a dedicated organization on
the Business plan, with a token holding `read`, `groups`, `policy`,
`enrollment` and `integrations`. CI runs it once the repository has
`UPDAWG_ACC_ORG` and `UPDAWG_ACC_TOKEN`.

### Releasing

`docs/` is generated by tfplugindocs from the schema and `examples/`
(`make generate`; CI fails on drift). A tag `v1.2.3` makes goreleaser build
and sign a **draft** release in the layout the Terraform and OpenTofu
registries read; publishing it is done by hand. The release workflow needs
the secrets `GPG_PRIVATE_KEY` and `GPG_PASSPHRASE`, the key registered with
the registries.

### The API client is generated

`internal/client/client.gen.go` is generated by
[oapi-codegen](https://github.com/oapi-codegen/oapi-codegen) from
`internal/client/openapi.json`, a copy of the spec the API serves. Never edit
it by hand.

```sh
make spec       # copy https://api.updawg.net/openapi.json
make generate   # regenerate client.gen.go
```

CI fails when `client.gen.go` is not what the copy generates. A daily job
fails when the copy is not what the API serves; that is the cue to run the
two commands above and read the diff.

Hand-written client code is in `internal/client/client.go`: the bearer
token, retrying a `429` after its `Retry-After`, and turning a problem
document into an error.

## Licence

MPL-2.0 once public. Private for now.
