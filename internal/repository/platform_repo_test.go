package repository

import (
	"reflect"
	"testing"

	"github.com/eandstravel/tenantcore/internal/models"
	"go.mongodb.org/mongo-driver/bson"
)

func TestListActiveFilterIsActiveOnly(t *testing.T) {
	want := bson.M{"status": models.PlatformUserActive}
	if got := listActiveFilter(); !reflect.DeepEqual(got, want) {
		t.Fatalf("listActiveFilter = %#v\nwant %#v", got, want)
	}
}
