package repository

import (
	"reflect"
	"testing"
	"time"

	"github.com/eandstravel/tenantcore/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// The repository methods need MongoDB, so they are exercised live. What can be
// pinned without one is the filters: they are where the rules live, and a
// filter that is subtly wrong still compiles and still returns documents.

func TestExpiringFilter(t *testing.T) {
	after := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	through := after.Add(7 * 24 * time.Hour)

	want := bson.M{
		// Only a subscription that is currently allowed to use the platform
		// has anything to lose, so cancelled and past-due are never warned.
		"status": bson.M{"$in": []models.SubscriptionStatus{
			models.SubscriptionActive, models.SubscriptionTrialing,
		}},
		// Exclusive at the low end (already lapsed is not "about to"),
		// inclusive at the high end (exactly seven days out is warned).
		"current_period_end": bson.M{"$gt": after, "$lte": through},
	}
	if got := expiringFilter(after, through); !reflect.DeepEqual(got, want) {
		t.Fatalf("expiringFilter = %#v\nwant %#v", got, want)
	}
}

func TestClaimFilter(t *testing.T) {
	id := primitive.NewObjectID()
	end := time.Date(2026, 10, 31, 2, 54, 42, 0, time.UTC)

	want := bson.M{
		"_id": id,
		// Matching the end date as well as the id means a renewal that lands
		// between the find and the claim makes the claim fail, instead of
		// marking the NEW period as already warned.
		"current_period_end": end,
		// $ne also matches a document with no marker at all, which is the
		// first-warning case.
		"expiry_notice_for": bson.M{"$ne": end},
	}
	if got := claimFilter(id, end); !reflect.DeepEqual(got, want) {
		t.Fatalf("claimFilter = %#v\nwant %#v", got, want)
	}
}

func TestReleaseFilter(t *testing.T) {
	id := primitive.NewObjectID()
	end := time.Date(2026, 10, 31, 2, 54, 42, 0, time.UTC)

	// Releasing only clears the marker for THIS period end, so a slow release
	// can never erase a newer period's marker.
	want := bson.M{"_id": id, "expiry_notice_for": end}
	if got := releaseFilter(id, end); !reflect.DeepEqual(got, want) {
		t.Fatalf("releaseFilter = %#v\nwant %#v", got, want)
	}
}
