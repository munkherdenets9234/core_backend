package repository

import (
	"reflect"
	"testing"
	"time"

	"github.com/eandstravel/tenantcore/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestPromoteLinkFilterRequiresUnpromoted(t *testing.T) {
	id := primitive.NewObjectID()
	want := bson.M{
		"_id": id,
		// Absent, not merely null: this is what makes two concurrent
		// promotes of one quote link exactly once.
		"promoted_tenant_id": bson.M{"$exists": false},
	}
	if got := promoteLinkFilter(id); !reflect.DeepEqual(got, want) {
		t.Fatalf("promoteLinkFilter = %#v\nwant %#v", got, want)
	}
}

func TestPromoteLinkUpdateSetsLinkStatusAndActor(t *testing.T) {
	tenantID := primitive.NewObjectID()
	userID := primitive.NewObjectID()
	now := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)

	got := promoteLinkUpdate(tenantID, &userID, now)
	want := bson.M{"$set": bson.M{
		"promoted_tenant_id": tenantID,
		"status":             models.QuoteClosed,
		"user_id":            &userID,
		"updated_at":         now,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("promoteLinkUpdate = %#v\nwant %#v", got, want)
	}

	set := promoteLinkUpdate(tenantID, nil, now)["$set"].(bson.M)
	if _, ok := set["user_id"]; ok {
		t.Fatalf("user_id must be absent when no actor is given: %#v", set)
	}
	if set["promoted_tenant_id"] != tenantID || set["status"] != models.QuoteClosed || set["updated_at"] != now {
		t.Fatalf("link, status and updated_at must be set without an actor: %#v", set)
	}
}
