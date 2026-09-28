package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// SitePage is the editable copy for one page of the operator's marketing
// site — the headlines, section labels and FAQ answers that were previously
// hardcoded in the site's own `lib/i18n` dictionaries and needed a deploy to
// change.
//
// The design rests on one decision: what is stored here is an OVERRIDE, not
// the content itself. The site keeps its compiled-in dictionaries and merges
// what it finds here on top. That is what makes the feature safe to add to a
// live site:
//
//   - A key nobody has edited is absent, and the site uses its own default.
//   - A key a developer adds in code works immediately, with no content
//     migration and no blank section waiting for someone to fill it in.
//   - tenantcore being unreachable degrades to the copy that shipped with
//     the build, rather than to an empty page.
//
// Seeding the current copy in (see cmd/seed-site-content) is therefore a
// convenience for whoever edits, not a requirement for the site to render.
type SitePage struct {
	ID primitive.ObjectID `bson:"_id,omitempty" json:"id"`

	// Page is the dictionary name the site uses: "home", "price",
	// "contact", "review", "ourProjects", "caseStudy", "common".
	Page string `bson:"page" json:"page"`

	Entries []ContentEntry `bson:"entries" json:"entries"`

	UpdatedAt time.Time           `bson:"updated_at" json:"updated_at"`
	UserID    *primitive.ObjectID `bson:"user_id,omitempty" json:"user_id,omitempty"`
}

// ContentEntry is one editable leaf, in every language at once.
//
// Path is the dotted route to it inside the page's dictionary, e.g.
// "hero.description" or "faq.items". Two things follow from that:
//
//   - Entries are a LIST, not a map keyed by path. A BSON field name
//     containing dots is legal in current MongoDB and a trap in every query
//     that then has to distinguish "the field a.b" from "field b inside a".
//     A list of {path, values} sidesteps it entirely.
//   - An ARRAY is a leaf, whatever it contains. "faq.items" is one entry
//     holding the whole list rather than "faq.items.0.q" and friends.
//     Otherwise the path language needs indices, expansion has to rebuild
//     arrays from sparse numeric keys, and deleting the second of three FAQ
//     entries becomes a renumbering problem.
type ContentEntry struct {
	Path string `bson:"path" json:"path"`

	// Values is the copy per language, keyed by ISO code — the same shape
	// as LocaleText, but `any` rather than string because a leaf may be a
	// string, a list of strings, or a list of small objects (an FAQ item is
	// {q, a}). A language missing from here falls back to the site's own
	// default for that language, not to another language's override.
	Values map[string]any `bson:"values" json:"values"`
}
