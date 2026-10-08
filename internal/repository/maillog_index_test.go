package repository

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
)

// The mail log holds recipient addresses, so it must not grow or keep them
// forever: rows delete themselves after mailLogTTL.
func TestMailLogTTLIndexIsDefined(t *testing.T) {
	var found bool
	for _, s := range indexSpecs() {
		if s.collection != mailLogCollection {
			continue
		}
		keys, ok := s.model.Keys.(bson.D)
		if !ok || len(keys) != 1 || keys[0].Key != "created_at" {
			continue
		}
		o := s.model.Options
		if o == nil || o.ExpireAfterSeconds == nil {
			t.Fatal("the created_at index on mail_log has no TTL")
		}
		if want := int32(30 * 24 * time.Hour / time.Second); *o.ExpireAfterSeconds != want {
			t.Fatalf("TTL = %d seconds, want %d (30 days)", *o.ExpireAfterSeconds, want)
		}
		found = true
	}
	if !found {
		t.Fatal("no TTL index on mail_log.created_at")
	}
}
