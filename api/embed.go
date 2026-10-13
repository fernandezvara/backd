// Package api embeds backd's HTTP API contract, which per-realm documents are derived from.
package api

import _ "embed"

// OpenAPI is api/openapi.yaml: the contract of the generic API (every route, with documents
// as open objects).
//
//go:embed openapi.yaml
var OpenAPI []byte
