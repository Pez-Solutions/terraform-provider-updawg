// Package client is the Updawg API, generated from openapi.json.
//
// openapi.json is a copy of the spec the API serves at
// https://api.updawg.net/openapi.json. Refresh it with `make spec`, then
// `go generate ./...`. CI fails when client.gen.go is not what the copy
// generates, and the scheduled spec job fails when the copy is not what the
// API serves.
package client

//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config oapi-codegen.yaml openapi.json
