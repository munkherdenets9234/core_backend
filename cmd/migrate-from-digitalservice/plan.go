package main

import (
	"fmt"
	"net/url"
	"strings"

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

// tenantDriftEntry names a tenant present in both databases whose key,
// domain or status differs. The key facet carries the api_key_last4 FIELD of
// each side (never a slice of, or anything derived from, the hash); domain and
// status are not secrets and print both values.
type tenantDriftEntry struct {
	ID   primitive.ObjectID
	Name string

	KeyDiffers  bool
	SourceLast4 string
	TargetLast4 string

	DomainDiffers bool
	SourceDomain  string
	TargetDomain  string

	StatusDiffers bool
	SourceStatus  string
	TargetStatus  string
}

// String is the one-line report for the tenant, listing every differing facet.
func (d tenantDriftEntry) String() string {
	s := fmt.Sprintf("drift id=%s name=%q", d.ID.Hex(), d.Name)
	if d.KeyDiffers {
		s += fmt.Sprintf(" key source=...%s target=...%s", d.SourceLast4, d.TargetLast4)
	}
	if d.DomainDiffers {
		s += fmt.Sprintf(" domain source=%s target=%s", d.SourceDomain, d.TargetDomain)
	}
	if d.StatusDiffers {
		s += fmt.Sprintf(" status source=%s target=%s", d.SourceStatus, d.TargetStatus)
	}
	return s
}

// tenantDrift lists tenants present in both slices whose api_key_hash, domain
// or status differ. Tenants on only one side, and tenants equal on all three,
// are omitted. The hash is compared but never copied into the result.
func tenantDrift(source, target []bson.M) []tenantDriftEntry {
	targetByID := make(map[primitive.ObjectID]bson.M, len(target))
	for _, doc := range target {
		if id, ok := doc["_id"].(primitive.ObjectID); ok {
			targetByID[id] = doc
		}
	}

	var out []tenantDriftEntry
	for _, doc := range source {
		id, ok := doc["_id"].(primitive.ObjectID)
		if !ok {
			continue
		}
		tgt, ok := targetByID[id]
		if !ok {
			continue
		}
		d := tenantDriftEntry{ID: id, Name: str(doc["name"])}
		if str(doc["api_key_hash"]) != str(tgt["api_key_hash"]) {
			d.KeyDiffers = true
			d.SourceLast4, d.TargetLast4 = str(doc["api_key_last4"]), str(tgt["api_key_last4"])
		}
		if sd, td := str(doc["domain"]), str(tgt["domain"]); sd != td {
			d.DomainDiffers, d.SourceDomain, d.TargetDomain = true, sd, td
		}
		if ss, ts := str(doc["status"]), str(tgt["status"]); ss != ts {
			d.StatusDiffers, d.SourceStatus, d.TargetStatus = true, ss, ts
		}
		if d.KeyDiffers || d.DomainDiffers || d.StatusDiffers {
			out = append(out, d)
		}
	}
	return out
}

// allCollections is the default and the full set of -collections names.
var allCollections = []string{"tenants", "platform_users", "plans", "subscriptions"}

// parseCollections turns the -collections flag into an ordered, de-duplicated
// list. Empty means all four; an unknown or empty name is an error so a typo
// cannot silently run less (or more) than the operator meant.
func parseCollections(s string) ([]string, error) {
	if strings.TrimSpace(s) == "" {
		return append([]string(nil), allCollections...), nil
	}
	valid := map[string]bool{}
	for _, c := range allCollections {
		valid[c] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		name := strings.TrimSpace(part)
		if !valid[name] {
			return nil, fmt.Errorf("unknown collection %q in -collections (valid: %s)", name, strings.Join(allCollections, ","))
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out, nil
}

// wouldInsertLine is the dry-run line for one document that would be inserted:
// collection, _id and a safe label only. Never hashes, emails or URIs, so
// platform_users carry the _id alone.
func wouldInsertLine(col string, id primitive.ObjectID, doc bson.M) string {
	line := fmt.Sprintf("would insert %s id=%s", col, id.Hex())
	switch col {
	case "tenants":
		line += fmt.Sprintf(" name=%q slug=%s", str(doc["name"]), str(doc["slug"]))
	case "plans":
		line += " slug=" + str(doc["slug"])
	case "subscriptions":
		if tid, ok := doc["tenant_id"].(primitive.ObjectID); ok {
			line += " tenant=" + tid.Hex()
		}
	}
	return line
}

// redactURI keeps only scheme, host and database of a connection string, so
// credentials and options never reach a log line. Anything unparseable prints
// as a placeholder rather than being echoed.
func redactURI(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<unparseable uri>"
	}
	return u.Scheme + "://" + u.Host + u.Path
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
