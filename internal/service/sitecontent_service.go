package service

import (
	"context"
	"errors"
	"strings"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/internal/repository"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// KnownPages are the dictionaries the marketing site has.
//
// A fixed list rather than free-form page names, because the site's
// dictionaries are compiled in: an override for a page that does not exist
// is copy nobody will ever see, written by someone who believed they had
// changed something. Adding a page here is a two-line change made at the
// same time as adding it to the site.
var KnownPages = []string{"common", "home", "price", "ourProjects", "caseStudy", "review", "contact"}

func knownPage(p string) bool {
	for _, k := range KnownPages {
		if k == p {
			return true
		}
	}
	return false
}

// SiteContentService owns the editable copy of the operator's marketing site.
//
// Everything it stores is an override over the site's own compiled-in
// defaults — see models.SitePage for why that matters more than it sounds.
type SiteContentService struct {
	repo *repository.SiteContentRepo
}

func NewSiteContentService(repo *repository.SiteContentRepo) *SiteContentService {
	return &SiteContentService{repo: repo}
}

// Get returns one page's overrides for the console. A page nobody has
// edited yet is an empty list, not a 404: the editor opens on it.
func (s *SiteContentService) Get(ctx context.Context, page string) (*models.SitePage, error) {
	if !knownPage(page) {
		return nil, apierr.NotFound("page")
	}
	p, err := s.repo.FindByPage(ctx, page)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return &models.SitePage{Page: page, Entries: []models.ContentEntry{}}, nil
		}
		return nil, apierr.Internal(err)
	}
	return p, nil
}

// All returns every page's overrides, for the public site's single fetch.
func (s *SiteContentService) All(ctx context.Context) ([]*models.SitePage, error) {
	out, err := s.repo.All(ctx)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return out, nil
}

// Save replaces a page's overrides.
//
// Entries with a blank path are rejected; entries whose every language is
// blank are DROPPED rather than stored. That is the mechanism for reverting
// a key to the site's default — clearing the field in the console and
// saving removes the override, instead of overriding the default with an
// empty string and blanking a section of a live page.
func (s *SiteContentService) Save(ctx context.Context, page string, entries []models.ContentEntry, userID *primitive.ObjectID) error {
	if !knownPage(page) {
		return apierr.NotFound("page")
	}

	kept := make([]models.ContentEntry, 0, len(entries))
	seen := map[string]bool{}
	for _, e := range entries {
		e.Path = strings.TrimSpace(e.Path)
		if e.Path == "" {
			return apierr.ValidationFailed("an entry has no path")
		}
		if seen[e.Path] {
			return apierr.ValidationFailed("duplicate path: " + e.Path)
		}
		seen[e.Path] = true

		values := map[string]any{}
		for lang, v := range e.Values {
			if !blank(v) {
				values[lang] = v
			}
		}
		if len(values) == 0 {
			continue
		}
		e.Values = values
		kept = append(kept, e)
	}

	if err := s.repo.Save(ctx, page, kept, userID); err != nil {
		return apierr.Internal(err)
	}
	return nil
}

// blank reports whether a leaf carries nothing worth storing. It has to
// handle the three shapes a leaf can take — a string, a list of strings, a
// list of objects — because "empty" means something different for each and
// the caller should not have to say which one it sent.
func blank(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}
