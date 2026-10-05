package main

import (
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// tenantAction is what the tenants step does with one source document.
type tenantAction int

const (
	// actionInsert: the tenant is not in the target yet.
	actionInsert tenantAction = iota
	// actionSkipExisting: the tenant is in the target and -only-missing is set,
	// so the target document is left exactly as it is.
	actionSkipExisting
	// actionOverwrite: the tenant is in the target and the default mode applies,
	// which upserts the source fields over it.
	actionOverwrite
)

// decideTenant is pure so the one rule that protects the target — never touch
// an existing tenant under -only-missing — is testable without a database.
func decideTenant(existsInTarget, onlyMissing bool) tenantAction {
	switch {
	case !existsInTarget:
		return actionInsert
	case onlyMissing:
		return actionSkipExisting
	default:
		return actionOverwrite
	}
}

// keyMismatch names a tenant whose api_key_hash differs between source and
// target. It carries the last four characters of each side and nothing else
// derived from the hash, so printing it discloses no usable material.
type keyMismatch struct {
	ID          primitive.ObjectID
	Name        string
	SourceLast4 string
	TargetLast4 string
}

// keyMismatches lists tenants present in both slices whose api_key_hash
// differ. Tenants found on only one side are not mismatches and are omitted.
func keyMismatches(source, target []bson.M) []keyMismatch {
	targetByID := make(map[primitive.ObjectID]bson.M, len(target))
	for _, doc := range target {
		if id, ok := doc["_id"].(primitive.ObjectID); ok {
			targetByID[id] = doc
		}
	}

	var out []keyMismatch
	for _, doc := range source {
		id, ok := doc["_id"].(primitive.ObjectID)
		if !ok {
			continue
		}
		tgt, ok := targetByID[id]
		if !ok {
			continue
		}
		sh, th := str(doc["api_key_hash"]), str(tgt["api_key_hash"])
		if sh == th {
			continue
		}
		out = append(out, keyMismatch{
			ID:          id,
			Name:        str(doc["name"]),
			SourceLast4: last4(sh),
			TargetLast4: last4(th),
		})
	}
	return out
}

// last4 returns up to the final four characters of a string value.
func last4(v any) string {
	s := str(v)
	if len(s) <= 4 {
		return s
	}
	return s[len(s)-4:]
}

// writeMode is how a tenant document is written to the target.
type writeMode int

const (
	// writeSet is the default upsert with $set, which overwrites.
	writeSet writeMode = iota
	// writeSetOnInsert is an upsert with $setOnInsert: the database itself
	// refuses to change a document that already exists.
	writeSetOnInsert
)

// writeKind pins the rule that -only-missing never writes with $set. The
// existence snapshot can go stale during a long run, so under -only-missing
// even a document decided as an insert is written in a form that cannot
// overwrite anything.
func writeKind(onlyMissing bool, _ tenantAction) writeMode {
	if onlyMissing {
		return writeSetOnInsert
	}
	return writeSet
}

// modeFor is the write mode for a collection. The collection is part of the
// signature so the rule "-only-missing never uses $set, for any collection"
// is stated and tested per collection rather than assumed.
func modeFor(col string, onlyMissing bool) writeMode {
	_ = col
	return writeKind(onlyMissing, actionInsert)
}

// summaryLine is the per-collection result under -only-missing. Counts only;
// never any document content.
func summaryLine(col string, inserted, skipped int) string {
	return fmt.Sprintf("%-15s inserted=%d skipped=%d", col, inserted, skipped)
}

// wouldInsert is the dry-run decision for one document under -only-missing:
// true when the target has no document with that id. It only informs counts;
// a real write never relies on it and always goes through $setOnInsert.
func wouldInsert(existing map[primitive.ObjectID]bool, id primitive.ObjectID) bool {
	return !existing[id]
}

// planLabel marks a dry-run line as a plan rather than a result.
func planLabel(dryRun bool, line string) string {
	if dryRun {
		return "would: " + line
	}
	return line
}
