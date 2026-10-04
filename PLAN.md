# terraform-provider-updawg

> *What's Updawg? Not much. Your servers are up to date.*

A Terraform provider for managing Updawg resources as code: groups, policies,
enrollment tokens and notification rules.

**Language:** Go (the Terraform plugin framework requires it) ·
**Licence:** MPL-2.0 · **Public** since 2026-10-04 ·
**Phase:** 3 — **started 2026-10-04** (DAWG-134 scaffold; see README.md)

> The design document marks this repository public, MPL-2.0, and explicitly
> "later". It exists now so the Portal API can be designed with it in mind rather
> than retrofitted for it. Expect no code here until Phase 3.

---

## 1. Why this repo exists

The target customer already manages their infrastructure as code — that is
precisely the sensibility that makes "Renovate for your servers" appealing. Those
teams will not hand-click maintenance windows and canary labels into a web UI and
call it managed.

There is also a deeper fit: Updawg's policies *are* code already. They're YAML
with a published JSON Schema, versioned server-side, with a preview of their
effect on the fleet. A Terraform provider is a short step from that, and it lets
a policy change go through the same review process as the infrastructure it
governs.

**Why Go, in an otherwise Rust product:** the Terraform plugin framework is Go.
That's the whole reason, and it's sufficient. Do not attempt a Rust provider.

## 2. How it fits with the other repos

```
  customer's Terraform  ──►  terraform-provider-updawg  ──► API token ──► api.updawg.net
                                                                          (updawg-server)
  updawg-docs   → provider usage guide (registry docs mirror it)
  updawg-server → the API this provider consumes; its OpenAPI spec is the contract
```

| Repo | Relationship |
|---|---|
| `updawg-server` | **The only dependency that matters.** Every resource here maps to Portal API endpoints. Those endpoints are public API the moment this provider ships — breaking them breaks customers' `terraform apply`. |
| `updawg-docs` | The provider guide lives in the docs site; the Terraform Registry docs are generated from this repo and should mirror it. |
| `updawg-portal` | Same resources, different surface. A resource managed by Terraform should be visibly marked as such in the portal, or people will edit it in the UI and be confused when it reverts. |
| `updawg-infra` | **Different thing, similar name.** `updawg-infra` runs Updawg's own cloud. This provider manages *customers'* Updawg configuration. Say so at the top of both READMEs. |

## 3. Planned resources and data sources

### Resources

| Resource | Maps to |
|---|---|
| `updawg_group` | `POST/PATCH/DELETE /orgs/{org}/groups`, `PUT .../hosts` for static membership |
| `updawg_policy` | `POST/PUT/DELETE /orgs/{org}/policies` — the YAML body, versioned on update |
| `updawg_enrollment_token` | `POST/DELETE /orgs/{org}/enrollment-tokens`, with default group, labels, expiry, max uses |
| `updawg_notification_channel` | `POST/PATCH/DELETE /orgs/{org}/channels` (Slack, Teams, email, ntfy, PagerDuty, webhook) |
| `updawg_notification_rule` | `POST /orgs/{org}/notification-rules` |
| `updawg_api_token` | `POST/DELETE /orgs/{org}/api-tokens` |
| `updawg_member` *(later)* | `PATCH/DELETE /orgs/{org}/members/{user_id}` |

### Data sources

`updawg_hosts` (filterable, so Terraform can drive labels from existing fleet
state), `updawg_group`, `updawg_policy`, `updawg_releases`, `updawg_organization`.

### Explicitly out of scope

Approving proposals, running jobs, managing rollouts. Those are **operations, not
desired state** — modelling a proposal approval as a Terraform resource would be
a category error. If automation is wanted there, it belongs in the API and
webhooks, not here.

## 4. Design notes that need settling early

- **Secrets in state.** Enrollment tokens and API tokens are shown once by the
  API and stored only as hashes server-side. That fits Terraform poorly. Decide
  the story before shipping: write-only attributes, ephemeral resources, or
  documenting plainly that the value lands in state and the state must be
  treated as a secret. Do not paper over this.
- **Policy drift.** A policy edited in the portal changes server-side version.
  The provider should surface that as drift, and the portal should show that a
  policy is Terraform-managed. Getting this wrong produces silent overwrite
  fights between a UI user and CI.
- **The policy body.** Keep it as raw YAML with schema validation at plan time
  (the API already has `POST /policies/validate`) rather than exploding it into
  HCL blocks. The YAML *is* the interface, it's documented, and HCL-ifying it
  creates a second schema to keep in sync forever.
- **Plan-time preview.** `POST /orgs/{org}/policies/preview` returns what a
  policy change would do to the current fleet. Surfacing that in `terraform plan`
  output would be genuinely excellent — and is the feature most likely to make
  this provider worth using over raw API calls.
- **Org scoping.** Provider-level `org` + API token, with per-resource override.
  MSPs manage several orgs from one configuration.

## 5. Proposed layout

```
terraform-provider-updawg/
├── main.go
├── internal/
│   ├── provider/            # provider config, client wiring
│   ├── resources/           # one file per resource
│   ├── datasources/
│   └── client/              # generated or hand-written API client
├── examples/                # used by tfplugindocs
├── docs/                    # generated registry documentation
├── templates/               # tfplugindocs templates
└── .goreleaser.yml          # signed release artifacts for the registry
```

Built on `terraform-plugin-framework` (not the legacy SDKv2). The API client
should be **generated from `updawg-server`'s `openapi.json`** so it cannot drift —
the same discipline the portal uses.

## 6. CI/CD

- `go vet`, `golangci-lint`, unit tests.
- **Acceptance tests** against a real (staging) Updawg org — these are the only
  tests that prove a provider works, and they need a dedicated test org and token
  in CI.
- `tfplugindocs` generation with a drift check.
- `goreleaser` with GPG-signed artifacts for Terraform Registry publication.

## 7. Prerequisites before starting

This repo stays empty until all of these are true:

1. The Portal API is stable enough to call public, with a committed OpenAPI spec.
2. API tokens and RBAC ship (Phase 2).
3. Policies, groups, enrollment tokens and notification rules have settled
   schemas — a provider ossifies whatever it wraps.
4. There is a customer asking for it. A provider with no users is a maintenance
   liability that also constrains the API forever.

## 8. Open questions

- Publish to the public Terraform Registry, or start as a private/manual install
  while the API stabilises? Registry publication is a compatibility commitment.
- Ephemeral resources vs. documented state-secret handling for tokens.
- Whether an OpenTofu registry listing happens at the same time. It should.
