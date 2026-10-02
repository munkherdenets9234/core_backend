// Package docs embeds the API contract so a deployment can serve it.
//
// The embed directive lives here, beside api.json, rather than in
// internal/api, because //go:embed cannot reach outside its own package
// directory. Keeping the spec at docs/api.json is worth a three-line package:
// that is where an integrator looks for it, and a spec buried under
// internal/ is one nobody finds.
package docs

import _ "embed"

// OpenAPI is the served contract. internal/api/openapi_test.go asserts it
// covers every route the router mounts, in both directions, so what this
// serves cannot drift from what the service does.
//
//go:embed api.json
var OpenAPI []byte
