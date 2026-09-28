package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// docs/api.json is hand-written, and a hand-written spec drifts. This test is
// what stops it: it walks the real router and asserts the two agree in both
// directions. A new endpoint fails the build until it is documented, and a
// path deleted from the code fails until it leaves the spec.
//
// It deliberately checks only that each method+path pair exists on both
// sides. Whether the documented schema matches the actual body is not
// something this can know. Coverage is the part that rots silently.

// undocumented are routes intentionally absent from the contract.
var undocumented = map[string]bool{
	// The contract endpoint serves the contract. Documenting it inside
	// itself is a curiosity, not information an integrator needs.
	"GET /docs/api.json": true,
}

type openapiDoc struct {
	Paths map[string]map[string]json.RawMessage `json:"paths"`
}

func loadSpec(t *testing.T) map[string]bool {
	t.Helper()

	// The test binary runs in its own package directory, so the spec is two
	// levels up. Resolved rather than assumed so the failure is "could not
	// find docs/api.json at <path>" instead of a confusing empty diff.
	path := filepath.Join("..", "..", "docs", "api.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		abs, _ := filepath.Abs(path)
		t.Fatalf("could not read the OpenAPI spec at %s: %v", abs, err)
	}

	var doc openapiDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("docs/api.json is not valid JSON: %v", err)
	}
	if len(doc.Paths) == 0 {
		t.Fatal("docs/api.json documents no paths")
	}

	documented := map[string]bool{}
	for path, item := range doc.Paths {
		for key := range item {
			method := strings.ToUpper(key)
			switch method {
			case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
				documented[method+" "+path] = true
			default:
				// "parameters", "summary", "description" and friends are
				// path-level keys, not operations.
			}
		}
	}
	return documented
}

// specPath converts a gin route into its OpenAPI form: ":id" becomes "{id}".
func specPath(ginPath string) string {
	parts := strings.Split(ginPath, "/")
	for i, part := range parts {
		if strings.HasPrefix(part, ":") {
			parts[i] = "{" + strings.TrimPrefix(part, ":") + "}"
		}
		if strings.HasPrefix(part, "*") {
			parts[i] = "{" + strings.TrimPrefix(part, "*") + "}"
		}
	}
	return strings.Join(parts, "/")
}

func TestEveryRouteIsDocumented(t *testing.T) {
	documented := loadSpec(t)
	engine, _ := testEngine(t)

	var missing []string
	for _, route := range engine.Routes() {
		key := route.Method + " " + specPath(route.Path)
		if undocumented[key] {
			continue
		}
		if !documented[key] {
			missing = append(missing, key)
		}
	}

	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("%d route(s) the router serves are missing from docs/api.json:\n  %s\n\n"+
			"Add them, or — if a route is genuinely not part of the contract — list it in `undocumented` with the reason.",
			len(missing), strings.Join(missing, "\n  "))
	}
}

func TestSpecDocumentsNoRouteThatIsGone(t *testing.T) {
	documented := loadSpec(t)
	engine, _ := testEngine(t)

	served := map[string]bool{}
	for _, route := range engine.Routes() {
		served[route.Method+" "+specPath(route.Path)] = true
	}

	var stale []string
	for key := range documented {
		if !served[key] {
			stale = append(stale, key)
		}
	}

	sort.Strings(stale)
	if len(stale) > 0 {
		t.Fatalf("%d path(s) in docs/api.json are not served by the router:\n  %s\n\n"+
			"A documented endpoint that does not exist is worse than an undocumented one — an integrator writes "+
			"against it and finds out in production.",
			len(stale), strings.Join(stale, "\n  "))
	}
}

func TestUndocumentedListIsNotStale(t *testing.T) {
	// An exemption for a route that no longer exists is a hole waiting for
	// the path to be reused — it would silently exempt the new endpoint too.
	engine, _ := testEngine(t)
	served := map[string]bool{}
	for _, route := range engine.Routes() {
		served[route.Method+" "+specPath(route.Path)] = true
	}

	for key := range undocumented {
		if !served[key] {
			t.Errorf("`undocumented` exempts %q, which the router does not serve", key)
		}
	}
}
