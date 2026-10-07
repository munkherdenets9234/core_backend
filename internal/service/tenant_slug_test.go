package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/pkg/apierr"
)

const slugRuleMessage = "slug must be lowercase letters, digits and hyphens, 1 to 63 characters"

func TestValidateSlug(t *testing.T) {
	valid := []string{"acme", "acme-travel-2", "a", strings.Repeat("a", 63), "2024", "a1-b2"}
	for _, s := range valid {
		if err := validateSlug(s); err != nil {
			t.Errorf("validateSlug(%q) = %v, want nil", s, err)
		}
	}
	invalid := []string{
		"My Shop", "Улаанбаатар", "acme_", "-acme", "acme-", "acme--travel",
		"", strings.Repeat("a", 64), "ACME", "acme.com", "ac me", "acme\n",
	}
	for _, s := range invalid {
		err := validateSlug(s)
		if err == nil {
			t.Errorf("validateSlug(%q) = nil, want an error", s)
			continue
		}
		wantStatus(t, err, http.StatusBadRequest)
		var ae *apierr.APIError
		want := slugRuleMessage
		if s == "" {
			want = "slug is required" // the existing empty-slug message
		}
		if errors.As(err, &ae) && ae.Message != want {
			t.Errorf("validateSlug(%q) message = %q", s, ae.Message)
		}
	}
}

// Create validates before it touches the repository, so a service with no
// repository is enough to prove a bad slug is refused and nothing is written.
func TestTenantCreateRejectsBadSlugBeforeStorage(t *testing.T) {
	s := &TenantService{}
	for _, slug := range []string{"My Shop", "ACME", "acme_", "-acme", "acme--travel", strings.Repeat("a", 64), "Улаанбаатар"} {
		tn := &models.Tenant{Name: "Acme", Slug: slug}
		_, key, err := s.Create(context.Background(), tn)
		if err == nil {
			t.Fatalf("Create(slug %q) succeeded", slug)
		}
		wantStatus(t, err, http.StatusBadRequest)
		if key != "" {
			t.Fatalf("Create(slug %q) returned a key", slug)
		}
		if tn.Slug != slug {
			t.Fatalf("slug %q was rewritten to %q: reject, do not coerce", slug, tn.Slug)
		}
	}
}

func TestTenantCreateTrimsThenValidatesSlug(t *testing.T) {
	if err := validateSlug(strings.TrimSpace(" acme ")); err != nil {
		t.Fatalf("trimmed slug rejected: %v", err)
	}
}
