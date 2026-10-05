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
