package view

import (
	"time"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/internal/service"
)

// This file adds a THIRD audience to the two described in view.go: the
// operator's own public marketing site.
//
// It gets its own view types rather than reusing the console's, because the
// two want opposite things from the same documents. The console edits and so
// needs every locale at once; the site renders one language and wants the
// copy already resolved. Serving the console's shape publicly would also
// leak the fields a superadmin edits — sort order, internal covers, which
// staff member last touched it.
//
// What the public NEVER sees, enforced by there being no field for it here:
// a tenant's contact email, its API key fragment, its status, and every
// entitlement field on a plan. A visitor reading the pricing page must not
// be able to learn which modules a tier grants.

// ── Pricing cards ─────────────────────────────────────────────────────────

// PublicPlan is one card on the marketing site's pricing page.
//
// Locale maps are sent whole and resolved in the browser, unlike a project:
// the site has a language toggle that must not refetch the price list to
// switch, and the price list is small enough that sending both languages
// costs nothing.
type PublicPlan struct {
	ID       string            `json:"id"`
	Slug     string            `json:"slug"`
	Name     map[string]string `json:"name"`
	Tagline  map[string]string `json:"tagline,omitempty"`
	Price    float64           `json:"price"`
	Currency string            `json:"currency"`
	// BillingNote is the "/month" or "one-time" suffix beside the price.
	BillingNote map[string]string   `json:"billing_note,omitempty"`
	Features    map[string][]string `json:"features"`
	Highlighted bool                `json:"highlighted"`
	SortOrder   int                 `json:"sort_order"`
	IsActive    bool                `json:"is_active"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
}

func PublicPlanOf(p *models.Plan) PublicPlan {
	out := PublicPlan{
		ID:        p.ID.Hex(),
		Slug:      p.Slug,
		Price:     p.Price,
		Currency:  p.Currency,
		SortOrder: p.SortOrder,
		IsActive:  p.IsActive,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
		// Rendered as {} and [] rather than null, for the same reason the
		// console's Plan view does it: a client that has to handle both is a
		// client with a bug waiting in it.
		Name:     map[string]string{},
		Features: map[string][]string{},
	}
	if m := p.Marketing; m != nil {
		if m.Name != nil {
			out.Name = m.Name
		}
		out.Tagline = m.Tagline
		out.BillingNote = m.BillingNote
		if m.Features != nil {
			out.Features = m.Features
		}
		out.Highlighted = m.Highlighted
	}
	// A plan with no marketing copy still has to render as something. The
	// slug is the one human-readable string always present, and showing it
	// beats an unnamed card on a public pricing page.
	if len(out.Name) == 0 {
		out.Name = map[string]string{"en": p.Name}
	}

	// English is guaranteed on both maps, because English is what every
	// consumer falls back TO. A client resolving `features[lang] ??
	// features.en` gets undefined when neither key exists — and an empty map
	// is exactly the case where that happens, so the map that looks safest
	// is the one that breaks the page. An empty list says "this tier lists
	// no features"; a missing key says nothing at all.
	if _, ok := out.Name["en"]; !ok {
		out.Name["en"] = p.Name
	}
	if _, ok := out.Features["en"]; !ok {
		out.Features["en"] = []string{}
	}
	return out
}

func PublicPlansOf(ps []*models.Plan) []PublicPlan {
	out := make([]PublicPlan, 0, len(ps))
	for _, p := range ps {
		out = append(out, PublicPlanOf(p))
	}
	return out
}

// ── Case studies ──────────────────────────────────────────────────────────

type ProjectImage struct {
	URL     string `json:"url"`
	Caption string `json:"caption"`
}

// ProjectMetric carries a resolved label, not a locale map — see
// PublicProject.
type ProjectMetric struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// PublicProject is one case study, already resolved to a single language.
//
// Resolved server-side, unlike a plan, because the prose is long: sending
// both languages of every description to render one of them would roughly
// double a page that is mostly text. The caller asks with ?lang= and the
// site fetches twice when it genuinely needs both.
type PublicProject struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	Tagline  string `json:"tagline"`
	Category string `json:"category"`
	// Description is the case study body, resolved for the requested lang.
	Description string `json:"description"`
	// LiveURL falls back to the tenant's bound domain when the case study
	// names no site of its own, so a tenant with a domain on file does not
	// have to have it typed twice.
	LiveURL    string          `json:"live_url,omitempty"`
	CoverImage *ProjectImage   `json:"cover_image,omitempty"`
	Images     []ProjectImage  `json:"images,omitempty"`
	Metrics    []ProjectMetric `json:"metrics,omitempty"`
	Featured   bool            `json:"featured"`
	SortOrder  int             `json:"sort_order"`
	CreatedAt  time.Time       `json:"created_at"`
}

func imageOf(i *models.Image) *ProjectImage {
	if i == nil || i.URL == "" {
		return nil
	}
	return &ProjectImage{URL: i.URL, Caption: i.Caption}
}

// PublicProjectOf resolves one case study for lang.
func PublicProjectOf(s service.Showcased, lang string) PublicProject {
	t, d := s.Tenant, s.Detail

	out := PublicProject{
		ID:          d.ID.Hex(),
		Name:        t.Name,
		Slug:        t.Slug,
		Tagline:     d.Tagline.Text(lang),
		Description: d.Description.Text(lang),
		Category:    d.Category,
		LiveURL:     d.WebsiteURL,
		CoverImage:  imageOf(d.CoverImage),
		Featured:    d.Featured,
		SortOrder:   d.SortOrder,
		CreatedAt:   d.CreatedAt,
	}
	if out.LiveURL == "" {
		out.LiveURL = t.Domain
	}

	for i := range d.Images {
		if img := imageOf(&d.Images[i]); img != nil {
			out.Images = append(out.Images, *img)
		}
	}
	for _, m := range d.Metrics {
		out.Metrics = append(out.Metrics, ProjectMetric{Label: m.Label.Text(lang), Value: m.Value})
	}
	return out
}

func PublicProjectsOf(ss []service.Showcased, lang string) []PublicProject {
	out := make([]PublicProject, 0, len(ss))
	for _, s := range ss {
		out = append(out, PublicProjectOf(s, lang))
	}
	return out
}

// ── Case studies, as the console edits them ───────────────────────────────

// TenantDetail is the console's view: every locale, every field, unresolved.
type TenantDetail struct {
	Tagline     models.LocaleText `json:"tagline,omitempty"`
	Description models.LocaleText `json:"description,omitempty"`
	Category    string            `json:"category,omitempty"`
	WebsiteURL  string            `json:"website_url,omitempty"`
	CoverImage  *models.Image     `json:"cover_image,omitempty"`
	AdminCover  *models.Image     `json:"admin_cover,omitempty"`
	Images      []models.Image    `json:"images,omitempty"`
	Metrics     []models.Metric   `json:"metrics,omitempty"`
	Showcase    bool              `json:"showcase"`
	Featured    bool              `json:"featured"`
	SortOrder   int               `json:"sort_order"`
}

// TenantDetailOf renders a case study, or nil when the tenant has none yet —
// which the console shows as an empty form rather than an error.
func TenantDetailOf(d *models.TenantDetail) *TenantDetail {
	if d == nil {
		return nil
	}
	return &TenantDetail{
		Tagline:     d.Tagline,
		Description: d.Description,
		Category:    d.Category,
		WebsiteURL:  d.WebsiteURL,
		CoverImage:  d.CoverImage,
		AdminCover:  d.AdminCover,
		Images:      d.Images,
		Metrics:     d.Metrics,
		Showcase:    d.Showcase,
		Featured:    d.Featured,
		SortOrder:   d.SortOrder,
	}
}

// ── Quotes ────────────────────────────────────────────────────────────────

// Quote is the console's view of a lead. There is no public counterpart:
// the contact form writes and never reads.
type Quote struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id,omitempty"`
	Name        string    `json:"name"`
	Email       string    `json:"email"`
	Phone       string    `json:"phone,omitempty"`
	CompanyName string    `json:"company_name,omitempty"`
	PackageSlug string    `json:"package_slug,omitempty"`
	Budget      string    `json:"budget,omitempty"`
	Timeline    string    `json:"timeline,omitempty"`
	Message     string    `json:"message,omitempty"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func QuoteOf(q *models.Quote) Quote {
	out := Quote{
		ID:          q.ID.Hex(),
		Name:        q.Name,
		Email:       q.Email,
		Phone:       q.Phone,
		CompanyName: q.CompanyName,
		PackageSlug: q.PackageSlug,
		Budget:      q.Budget,
		Timeline:    q.Timeline,
		Message:     q.Message,
		Status:      string(q.Status),
		CreatedAt:   q.CreatedAt,
		UpdatedAt:   q.UpdatedAt,
	}
	if q.TenantID != nil {
		out.TenantID = q.TenantID.Hex()
	}
	return out
}

func QuotesOf(qs []*models.Quote) []Quote {
	out := make([]Quote, 0, len(qs))
	for _, q := range qs {
		out = append(out, QuoteOf(q))
	}
	return out
}

// ── Editable site copy ────────────────────────────────────────────────────

// SiteContent is every page's overrides for ONE language, as the marketing
// site fetches them: {page: {dotted.path: value}}.
//
// Resolved per language like a project rather than sent whole like a plan,
// and for the same reason — this is the page's prose, and the site asks
// again when the visitor switches language.
//
// A path absent here is not empty copy: it means nobody has overridden that
// key and the site should use the default it shipped with. Sending an empty
// string instead would blank the section.
type SiteContent map[string]map[string]any

func SiteContentOf(pages []*models.SitePage, lang string) SiteContent {
	out := SiteContent{}
	for _, p := range pages {
		vals := map[string]any{}
		for _, e := range p.Entries {
			if v, ok := e.Values[lang]; ok && v != nil {
				vals[e.Path] = v
			}
		}
		if len(vals) > 0 {
			out[p.Page] = vals
		}
	}
	return out
}

// SitePage is the console's view: every language at once, because the
// editor shows English and Mongolian side by side.
type SitePage struct {
	Page    string                `json:"page"`
	Entries []models.ContentEntry `json:"entries"`
}

func SitePageOf(p *models.SitePage) SitePage {
	entries := p.Entries
	if entries == nil {
		entries = []models.ContentEntry{}
	}
	return SitePage{Page: p.Page, Entries: entries}
}
