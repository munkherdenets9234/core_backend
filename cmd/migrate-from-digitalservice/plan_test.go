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

func TestKeyMismatches_ReportsOnlyDifferingAndOnlyLast4(t *testing.T) {
	a, b, c, d := primitive.NewObjectID(), primitive.NewObjectID(), primitive.NewObjectID(), primitive.NewObjectID()
	source := []bson.M{
		{"_id": a, "name": "Same", "api_key_hash": "hash-same-0000"},
		{"_id": b, "name": "Differs", "api_key_hash": "hash-source-ABCD"},
		{"_id": c, "name": "SourceOnly", "api_key_hash": "hash-only-src-1111"},
	}
	target := []bson.M{
		{"_id": a, "name": "Same", "api_key_hash": "hash-same-0000"},
		{"_id": b, "name": "Differs", "api_key_hash": "hash-target-WXYZ"},
		{"_id": d, "name": "TargetOnly", "api_key_hash": "hash-only-tgt-2222"},
	}

	got := keyMismatches(source, target)
	if len(got) != 1 {
		t.Fatalf("got %d mismatches, want 1: %+v", len(got), got)
	}
	m := got[0]
	if m.ID != b || m.Name != "Differs" || m.SourceLast4 != "ABCD" || m.TargetLast4 != "WXYZ" {
		t.Fatalf("unexpected mismatch: %+v", m)
	}
	for _, s := range []string{m.Name, m.SourceLast4, m.TargetLast4} {
		if strings.Contains(s, "hash-") {
			t.Fatalf("report field leaks hash material: %q", s)
		}
	}
}

func TestLast4_ShortAndMissingValues(t *testing.T) {
	if got := last4("ab"); got != "ab" {
		t.Errorf("got %q", got)
	}
	if got := last4(nil); got != "" {
		t.Errorf("got %q", got)
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
