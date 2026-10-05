package service

import (
	"encoding/json"
	"testing"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// TestTenantIdentityWireContract pins the exact JSON a product service decodes
// from the tenant-by-key response. digitalservice's internal/tenantresolve has
// a test that decodes this SAME literal, so changing a field name, its order or
// the nil-Hosts-as-[] rule here fails that test there (and the reverse). Change
// both literals together, deliberately.
func TestTenantIdentityWireContract(t *testing.T) {
	id, err := primitive.ObjectIDFromHex("507f1f77bcf86cd799439011")
	if err != nil {
		t.Fatal(err)
	}
	// ResolveByAPIKey normalises a nil host list to an empty one; mirror that.
	var hosts []string
	if hosts == nil {
		hosts = []string{}
	}
	got, err := json.Marshal(TenantIdentity{
		TenantID: id,
		Slug:     "acme",
		Name:     "Acme",
		Status:   "active",
		Domain:   "acme.example",
		Hosts:    hosts,
	})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"tenant_id":"507f1f77bcf86cd799439011","slug":"acme","name":"Acme","status":"active","domain":"acme.example","hosts":[]}`
	if string(got) != want {
		t.Fatalf("wire shape changed:\n got %s\nwant %s", got, want)
	}
}
