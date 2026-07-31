package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"sigs.k8s.io/agent-sandbox/terraform/internal/provider"
)

// version is set by goreleaser via ldflags at release time.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/glottman/agentsandbox",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
