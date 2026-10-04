// Command terraform-provider-updawg manages an Updawg organization's
// configuration — groups, policies, enrollment tokens, notifications — from
// Terraform or OpenTofu.
//
// Not to be confused with updawg-infra, which runs Updawg's own cloud.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/Pez-Solutions/terraform-provider-updawg/internal/provider"
)

// version is set by goreleaser at release time.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/pez-solutions/updawg",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}

// Registry documentation, from the schema and examples/. CI fails when docs/
// is not what this generates.
//go:generate go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@v0.25.0 generate --provider-name updawg
