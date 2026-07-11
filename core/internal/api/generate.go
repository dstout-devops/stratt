// Package api serves the control-plane API. The OpenAPI document at
// core/api/openapi.yaml is the source of truth (ADR-0006); this package's
// generated code is regenerated with `task generate` and diff-checked in CI.
package api

//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.7.2 -config oapi-codegen.yaml ../../api/openapi.yaml
