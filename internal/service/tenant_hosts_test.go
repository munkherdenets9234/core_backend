package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

type fakeHostStore struct {
	owner map[string]primitive.ObjectID
	saved []string
	dup   bool
}

func (f *fakeHostStore) FindByHost(_ context.Context, h string) (*models.Tenant, error) {
	id, ok := f.owner[h]
	if !ok {
		return nil, mongo.ErrNoDocuments
	}
	return &models.Tenant{ID: id}, nil
}

func (f *fakeHostStore) UpdateHosts(_ context.Context, _ primitive.ObjectID, hosts []string) error {
	if f.dup {
		return mongo.WriteException{WriteErrors: mongo.WriteErrors{{Code: 11000}}}
	}
	f.saved = hosts
	return nil
}

func TestSetHostsNormalisesAndDedupes(t *testing.T) {
	st := &fakeHostStore{owner: map[string]primitive.ObjectID{}}
	err := setHosts(context.Background(), st, primitive.NewObjectID(),
		[]string{"Tower.Example.com:443", "tower.example.com.", " ", "b.example.com"})
	if err != nil {
		t.Fatalf("setHosts: %v", err)
	}
	if len(st.saved) != 2 || st.saved[0] != "tower.example.com" || st.saved[1] != "b.example.com" {
		t.Fatalf("saved = %v", st.saved)
	}
}

func TestSetHostsOwnedByAnotherTenantIs409(t *testing.T) {
	st := &fakeHostStore{owner: map[string]primitive.ObjectID{"tower.example.com": primitive.NewObjectID()}}
	err := setHosts(context.Background(), st, primitive.NewObjectID(), []string{"tower.example.com"})
	var ae *apierr.APIError
	if !errors.As(err, &ae) || ae.HTTPStatus != 409 {
		t.Fatalf("want 409, got %v", err)
	}
	if st.saved != nil {
		t.Fatal("nothing should be saved on conflict")
	}
}

func TestSetHostsOwnHostIsNotAConflict(t *testing.T) {
	id := primitive.NewObjectID()
	st := &fakeHostStore{owner: map[string]primitive.ObjectID{"tower.example.com": id}}
	if err := setHosts(context.Background(), st, id, []string{"tower.example.com"}); err != nil {
		t.Fatalf("re-saving own host: %v", err)
	}
}

// The pre-check is racy; the unique index is the real guard.
func TestSetHostsRaceLostToTheIndexIs409(t *testing.T) {
	st := &fakeHostStore{owner: map[string]primitive.ObjectID{}, dup: true}
	err := setHosts(context.Background(), st, primitive.NewObjectID(), []string{"x.example.com"})
	var ae *apierr.APIError
	if !errors.As(err, &ae) || ae.HTTPStatus != 409 {
		t.Fatalf("want 409, got %v", err)
	}
}

func TestSetHostsRejectsMalformedHosts(t *testing.T) {
	tooLong := strings.Repeat("a", 250) + ".com" // 254 chars
	many := make([]string, 21)
	for i := range many {
		many[i] = fmt.Sprintf("h%d.example.com", i)
	}
	cases := map[string][]string{
		"url":        {"https://x.com/p"},
		"whitespace": {"a b"},
		"wildcard":   {"*.x.com"},
		"userinfo":   {"u@x.com"},
		"query":      {"x.com?a=1"},
		"fragment":   {"x.com#a"},
		"backslash":  {`x.com\p`},
		"too long":   {tooLong},
		"21 hosts":   many,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			st := &fakeHostStore{owner: map[string]primitive.ObjectID{}}
			err := setHosts(context.Background(), st, primitive.NewObjectID(), in)
			var ae *apierr.APIError
			if !errors.As(err, &ae) || ae.HTTPStatus != 400 {
				t.Fatalf("want 400, got %v", err)
			}
			if st.saved != nil {
				t.Fatal("nothing should be saved")
			}
		})
	}

	st := &fakeHostStore{owner: map[string]primitive.ObjectID{}}
	if err := setHosts(context.Background(), st, primitive.NewObjectID(), many[:20]); err != nil {
		t.Fatalf("20 hosts is the cap and must be accepted: %v", err)
	}
}

// Only a bare DNS name in ASCII (after IDNA) or a real IP literal is stored.
// Anything else would never equal a Host header the realestate consumer
// normalises, or would carry markup and separators into the console.
func TestSetHostsRejectsNonHostCharacters(t *testing.T) {
	for _, h := range []string{
		"[::1", "[x.com]", "a<b.com", "a,b.com", "a%2e.com", "fe80::1%eth0", "[fe80::1%25eth0]",
		"-a.com", "a-.com", "a..com", ".a.com", "a_b.com", "a;b.com", "a'b.com", `a"b.com`,
		"999.1.1.1", "1.2.3", strings.Repeat("a", 64) + ".com", "x.com:abc", "x.com:",
		"a\x00.com", "a​.com",
	} {
		st := &fakeHostStore{owner: map[string]primitive.ObjectID{}}
		err := setHosts(context.Background(), st, primitive.NewObjectID(), []string{h})
		var ae *apierr.APIError
		if !errors.As(err, &ae) || ae.HTTPStatus != 400 {
			t.Errorf("%q: want 400, got %v (saved %v)", h, err, st.saved)
		}
	}
}

func TestSetHostsAcceptsHostsAndIPLiterals(t *testing.T) {
	cases := map[string]string{
		"Tower.Example.com:443":  "tower.example.com",
		"localhost:3000":         "localhost",
		"127.0.0.1":              "127.0.0.1",
		"127.0.0.1:8080":         "127.0.0.1",
		"::1":                    "::1",
		"[::1]:443":              "::1",
		"[2001:DB8::1]":          "2001:db8::1",
		"münchen.example":        "xn--mnchen-3ya.example",
		"xn--mnchen-3ya.example": "xn--mnchen-3ya.example",
		"a-b.c0.example.":        "a-b.c0.example",
	}
	for in, want := range cases {
		st := &fakeHostStore{owner: map[string]primitive.ObjectID{}}
		if err := setHosts(context.Background(), st, primitive.NewObjectID(), []string{in}); err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if len(st.saved) != 1 || st.saved[0] != want {
			t.Errorf("%q: saved %v, want %q", in, st.saved, want)
		}
	}
}
