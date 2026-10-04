.PHONY: spec generate lint test test-tofu

# Copy the spec the API serves. Then `make generate`.
spec:
	curl -fsS https://api.updawg.net/openapi.json -o internal/client/openapi.json

generate:
	go generate ./...

lint:
	golangci-lint run ./...

# The provider tests run the real CLI. Terraform by default; `make test-tofu`
# for OpenTofu.
test:
	go test -count=1 ./...

test-tofu:
	TF_ACC_TERRAFORM_PATH="$$(command -v tofu)" TF_ACC_PROVIDER_NAMESPACE=hashicorp go test -count=1 ./...
