package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/dstout-devops/stratt/core/internal/awximport"
	"github.com/dstout-devops/stratt/core/internal/connectors/awx"
)

// runImport dispatches `stratt import <source>`. Only awx is supported (the
// charter's frozen 24.6.1 migration target, §5.6).
func runImport(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: stratt import awx --endpoint <url> [--token <t>] -o <out-dir>")
	}
	switch args[0] {
	case "awx":
		return runImportAWX(args[1:])
	default:
		return fmt.Errorf("unknown import source %q (supported: awx)", args[0])
	}
}

func runImportAWX(args []string) error {
	fs := flag.NewFlagSet("import awx", flag.ExitOnError)
	endpoint := fs.String("endpoint", "", "AWX base URL, e.g. https://awx.example.com")
	// Token defaults from env so it need not appear in shell history.
	token := fs.String("token", envOr("STRATT_AWX_TOKEN", ""), "AWX API token (default $STRATT_AWX_TOKEN)")
	out := fs.String("o", "", "output directory for the migration bundle (must be fresh)")
	_ = fs.Parse(args)

	if *endpoint == "" || *out == "" {
		return fmt.Errorf("import awx: --endpoint and -o are required")
	}

	client := awx.New(awx.Config{Endpoint: *endpoint, Token: *token})
	snap, err := client.Enumerate(context.Background())
	if err != nil {
		return err
	}
	emit, err := awximport.Bundle(snap, awximport.Options{})
	if err != nil {
		return err
	}
	if err := awximport.WriteBundle(*out, emit); err != nil {
		return err
	}

	fmt.Printf("stratt: wrote %d declaration file(s) to %s\n", len(emit.Files), *out)
	fmt.Printf("stratt: review %s/migration-report.md, then run: stratt plan -d %s\n", *out, *out)
	return nil
}
