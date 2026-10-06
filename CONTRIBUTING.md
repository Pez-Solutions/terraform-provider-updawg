# Contributing

Issues and pull requests are welcome. **Security problems are not issues**:
please report them privately as https://updawg.net/security/ describes.

## Pull requests

- `main` is protected. Every change goes through a pull request, CI must
  pass, and merges are squashed.
- Title the pull request with what changes, in plain words and sentence case,
  e.g. `Add the hosts data source`. No `feat:`/`fix:` prefixes. The body says
  why, and what you tested.
- One change per pull request.

## Building and testing

`make generate` regenerates the client from `internal/client/openapi.json`, and
CI fails if it drifts. `make test` runs the unit tests against Terraform,
`make test-tofu` against OpenTofu. Acceptance tests (`TF_ACC=1`) need an
Updawg organization and an API token. See the README.

Dependencies are updated weekly by Dependabot.
