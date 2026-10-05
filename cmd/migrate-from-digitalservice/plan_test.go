package main

import (
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestDecideTenant_MissingInsertsRegardlessOfMode(t *testing.T) {
	for _, onlyMissing := range []bool{false, true} {
		if got := decideTenant(false, onlyMissing); got != actionInsert {
			t.Errorf("onlyMissing=%v: got %v, want actionInsert", onlyMissing, got)
		}
	}
}

func TestDecideTenant_ExistingWithOnlyMissingSkips(t *testing.T) {
	if got := decideTenant(true, true); got != actionSkipExisting {
		t.Fatalf("got %v, want actionSkipExisting", got)
	}
}

func TestDecideTenant_ExistingDefaultOverwrites(t *testing.T) {
	if got := decideTenant(true, false); got != actionOverwrite {
		t.Fatalf("got %v, want actionOverwrite", got)
	}
}

func TestWriteKind_OnlyMissingNeverUsesSet(t *testing.T) {
	for _, a := range []tenantAction{actionInsert, actionSkipExisting, actionOverwrite} {
		if got := writeKind(true, a); got != writeSetOnInsert {
			t.Errorf("onlyMissing action=%v: got %v, want writeSetOnInsert", a, got)
		}
	}
}

func TestWriteKind_DefaultModeUsesSet(t *testing.T) {
	for _, a := range []tenantAction{actionInsert, actionOverwrite} {
		if got := writeKind(false, a); got != writeSet {
			t.Errorf("default action=%v: got %v, want writeSet", a, got)
		}
	}
}

func TestModeFor_OnlyMissingNeverUsesSetForAnyCollection(t *testing.T) {
	for _, col := range []string{"tenants", "platform_users", "plans", "subscriptions"} {
		if got := modeFor(col, true); got != writeSetOnInsert {
			t.Errorf("%s with onlyMissing: got %v, want writeSetOnInsert", col, got)
		}
		if got := modeFor(col, false); got != writeSet {
			t.Errorf("%s default: got %v, want writeSet", col, got)
		}
	}
}

func TestSummaryLine_CountsOnly(t *testing.T) {
	got := summaryLine("plans", 3, 7)
	for _, want := range []string{"plans", "inserted=3", "skipped=7"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "hash") || strings.Contains(got, "{") {
		t.Errorf("summary carries document content: %q", got)
	}
}

func TestWouldInsert_DecidesFromExistingIDSet(t *testing.T) {
	have, missing := primitive.NewObjectID(), primitive.NewObjectID()
	existing := map[primitive.ObjectID]bool{have: true}
	if wouldInsert(existing, have) {
		t.Error("existing id must be skipped, not inserted")
	}
	if !wouldInsert(existing, missing) {
		t.Error("absent id must be inserted")
	}
	if !wouldInsert(nil, missing) {
		t.Error("empty target must insert")
	}
}

func TestPlanLabel_OnlyInDryRun(t *testing.T) {
	if got := planLabel(true, "x"); got != "would: x" {
		t.Errorf("got %q", got)
	}
	if got := planLabel(false, "x"); got != "x" {
		t.Errorf("got %q", got)
	}
}

func TestTenantDrift_ReportsAllFacetsAndNoHash(t *testing.T) {
	a, b, c, d, e := primitive.NewObjectID(), primitive.NewObjectID(), primitive.NewObjectID(), primitive.NewObjectID(), primitive.NewObjectID()
	source := []bson.M{
		{"_id": a, "name": "Same", "api_key_hash": "hash-same-0000", "api_key_last4": "0000", "domain": "same.example", "status": "active"},
		{"_id": b, "name": "Differs", "api_key_hash": "hash-source-ABCD", "api_key_last4": "ABCD", "domain": "old.example", "status": "active"},
		{"_id": c, "name": "SourceOnly", "api_key_hash": "hash-only-src-1111", "api_key_last4": "1111", "domain": "x.example", "status": "active"},
		{"_id": e, "name": "StatusOnly", "api_key_hash": "hash-e-9999", "api_key_last4": "9999", "domain": "e.example", "status": "active"},
	}
	target := []bson.M{
		{"_id": a, "name": "Same", "api_key_hash": "hash-same-0000", "api_key_last4": "0000", "domain": "same.example", "status": "active"},
		{"_id": b, "name": "Differs", "api_key_hash": "hash-target-WXYZ", "api_key_last4": "WXYZ", "domain": "new.example", "status": "suspended"},
		{"_id": d, "name": "TargetOnly", "api_key_hash": "hash-only-tgt-2222", "api_key_last4": "2222", "domain": "y.example", "status": "active"},
		{"_id": e, "name": "StatusOnly", "api_key_hash": "hash-e-9999", "api_key_last4": "9999", "domain": "e.example", "status": "suspended"},
	}

	got := tenantDrift(source, target)
	if len(got) != 2 {
		t.Fatalf("got %d drifted tenants, want 2: %+v", len(got), got)
	}
	var lines []string
	for _, dr := range got {
		lines = append(lines, dr.String())
	}
	all := strings.Join(lines, "\n")
	for _, want := range []string{
		b.Hex(), `"Differs"`, "key source=...ABCD target=...WXYZ",
		"domain source=old.example target=new.example",
		"status source=active target=suspended",
		e.Hex(), `"StatusOnly"`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("report missing %q in:\n%s", want, all)
		}
	}
	if strings.Contains(lines[1], "key ") || strings.Contains(lines[1], "domain") {
		t.Errorf("status-only drift reports other facets: %q", lines[1])
	}
	if strings.Contains(all, "hash") {
		t.Errorf("report leaks hash material:\n%s", all)
	}
	for _, id := range []primitive.ObjectID{a, c, d} {
		if strings.Contains(all, id.Hex()) {
			t.Errorf("equal or one-sided tenant %s reported", id.Hex())
		}
	}
}

func TestParseCollections(t *testing.T) {
	got, err := parseCollections("")
	if err != nil || len(got) != 4 {
		t.Fatalf("default: %v %v", got, err)
	}
	got, err = parseCollections(" tenants , plans,tenants")
	if err != nil || len(got) != 2 || got[0] != "tenants" || got[1] != "plans" {
		t.Fatalf("subset: %v %v", got, err)
	}
	for _, bad := range []string{"tenants,bogus", "packages", ",", "tenants,,plans"} {
		if _, err := parseCollections(bad); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

func TestWouldInsertLine_SafeLabelsOnly(t *testing.T) {
	id := primitive.NewObjectID()
	tid := primitive.NewObjectID()
	cases := []struct {
		col  string
		doc  bson.M
		want []string
	}{
		{"tenants", bson.M{"name": "Acme", "slug": "acme", "api_key_hash": "hash-secret", "contact_email": "a@b.example"}, []string{`name="Acme"`, "slug=acme"}},
		{"plans", bson.M{"slug": "pro", "name": "Pro"}, []string{"slug=pro"}},
		{"subscriptions", bson.M{"tenant_id": tid, "plan_id": id}, []string{"tenant=" + tid.Hex()}},
		{"platform_users", bson.M{"email": "who@example.test", "password_hash": "hash-pw"}, nil},
	}
	for _, c := range cases {
		line := wouldInsertLine(c.col, id, c.doc)
		for _, w := range append([]string{c.col, id.Hex()}, c.want...) {
			if !strings.Contains(line, w) {
				t.Errorf("%s: %q missing %q", c.col, line, w)
			}
		}
		for _, bad := range []string{"hash", "@"} {
			if strings.Contains(line, bad) {
				t.Errorf("%s: line leaks %q: %q", c.col, bad, line)
			}
		}
	}
}

func TestRedactURI_DropsUserInfo(t *testing.T) {
	got := redactURI("mongodb://fakeuser:fakepass@db.example.test:27017/fakedb?retryWrites=true")
	if strings.Contains(got, "fakeuser") || strings.Contains(got, "fakepass") || strings.Contains(got, "@") || strings.Contains(got, "retryWrites") {
		t.Fatalf("not redacted: %q", got)
	}
	if !strings.Contains(got, "mongodb://") || !strings.Contains(got, "db.example.test:27017") || !strings.Contains(got, "fakedb") {
		t.Fatalf("lost scheme/host/db: %q", got)
	}
	if got := redactURI("%%not a uri"); strings.Contains(got, "not a uri") {
		t.Fatalf("unparseable input echoed: %q", got)
	}
}
