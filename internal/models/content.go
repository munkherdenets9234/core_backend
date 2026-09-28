package models

// What this file holds is a deliberate exception to the rule stated at the
// top of platform.go — that tenantcore owns only what every product must
// agree on, and never what a product sells.
//
// A tenant's case study and the leads it receives are not that. They are the
// PLATFORM OPERATOR's own content: Inno Nomads showing prospective customers
// who it has onboarded, and collecting enquiries from people who are not
// tenants of anything yet. The operator's marketing site and its console are
// the only readers and writers. No product service ever sees any of this —
// there is no view for it on the svc surface, exactly as with plans.
//
// The distinction that matters: a tenant's own storefront content still
// belongs to the product serving it. This is the operator's storefront.

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Image is a picture with its caption, stored as a URL because the bytes
// live in Cloudinary — uploaded by the console directly, never through this
// service.
type Image struct {
	URL     string `bson:"url" json:"url"`
	Caption string `bson:"caption" json:"caption"`
}

// Metric is one "73% faster" style figure on a case study. The label is
// bilingual; the value is not, because numbers and units read the same in
// both languages here.
type Metric struct {
	Label LocaleText `bson:"label" json:"label"`
	Value string     `bson:"value" json:"value"`
}

// TenantDetail is the operator's case study about one tenant.
//
// One per tenant, keyed by tenant_id rather than embedded on the tenant
// document: a tenant is a billing and identity record read on every product
// request, and hanging a dozen images and two languages of prose off it
// would make the hot path carry the marketing site's payload.
//
// Showcase and Featured are separate on purpose. Showcase is "may the public
// see this at all"; Featured is "does it lead the page". Collapsing them
// means un-featuring something silently unpublishes it.
type TenantDetail struct {
	ID       primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	TenantID primitive.ObjectID `bson:"tenant_id" json:"tenant_id"`

	Tagline     LocaleText `bson:"tagline,omitempty" json:"tagline,omitempty"`
	Description LocaleText `bson:"description,omitempty" json:"description,omitempty"`
	Category    string     `bson:"category,omitempty" json:"category,omitempty"`

	// WebsiteURL is the tenant's own live site. The public read falls back to
	// the tenant's bound Domain when this is blank, so a tenant with a domain
	// on file does not need it entered twice.
	WebsiteURL string `bson:"website_url,omitempty" json:"website_url,omitempty"`

	// CoverImage is the case-study page's hero. AdminCover is curated
	// separately for list and front-page display, because the image that
	// works as a full-bleed banner rarely works as a card thumbnail.
	CoverImage *Image `bson:"cover_image,omitempty" json:"cover_image,omitempty"`
	AdminCover *Image `bson:"admin_cover,omitempty" json:"admin_cover,omitempty"`

	Images  []Image  `bson:"images,omitempty" json:"images,omitempty"`
	Metrics []Metric `bson:"metrics,omitempty" json:"metrics,omitempty"`

	Showcase  bool `bson:"showcase" json:"showcase"`
	Featured  bool `bson:"featured" json:"featured"`
	SortOrder int  `bson:"sort_order" json:"sort_order"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`

	// UserID is the platform user who last edited this. Same reason as on a
	// plan: "who changed this" is the first question asked afterwards.
	UserID *primitive.ObjectID `bson:"user_id,omitempty" json:"user_id,omitempty"`
}

type QuoteStatus string

const (
	QuoteNew       QuoteStatus = "new"
	QuoteContacted QuoteStatus = "contacted"
	QuoteQuoted    QuoteStatus = "quoted"
	QuoteClosed    QuoteStatus = "closed"
)

// Valid reports whether s is a status this service recognises. An unknown
// value arriving from a console is a bug in the console, and storing it
// would make every later filter quietly wrong.
func (s QuoteStatus) Valid() bool {
	switch s {
	case QuoteNew, QuoteContacted, QuoteQuoted, QuoteClosed:
		return true
	}
	return false
}

// Quote is an enquiry from the public contact form.
//
// TenantID is optional and usually absent. Most quotes come from prospects
// who are not tenants of anything — that is the entire point of the form —
// so this is nullable rather than the required foreign key it looks like it
// should be. A quote that DOES carry one came through an existing tenant's
// own storefront.
//
// PackageSlug is a slug, not a plan id: it records which pricing card the
// visitor clicked, and must survive that plan being renamed, re-slugged or
// deleted. Resolving it to a live plan at read time would turn a historical
// fact into a dangling reference.
type Quote struct {
	ID       primitive.ObjectID  `bson:"_id,omitempty" json:"id"`
	TenantID *primitive.ObjectID `bson:"tenant_id,omitempty" json:"tenant_id,omitempty"`

	Name        string `bson:"name" json:"name"`
	Email       string `bson:"email" json:"email"`
	Phone       string `bson:"phone,omitempty" json:"phone,omitempty"`
	CompanyName string `bson:"company_name,omitempty" json:"company_name,omitempty"`

	PackageSlug string `bson:"package_slug,omitempty" json:"package_slug,omitempty"`
	Budget      string `bson:"budget,omitempty" json:"budget,omitempty"`
	Timeline    string `bson:"timeline,omitempty" json:"timeline,omitempty"`
	Message     string `bson:"message,omitempty" json:"message,omitempty"`

	Status    QuoteStatus `bson:"status" json:"status"`
	CreatedAt time.Time   `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time   `bson:"updated_at" json:"updated_at"`

	UserID *primitive.ObjectID `bson:"user_id,omitempty" json:"user_id,omitempty"`
}

// TenantPlan assigns a pricing card to a tenant's own storefront.
//
// This is NOT a subscription and does not affect entitlement in any way. A
// subscription is what the tenant pays the platform; this is which of the
// platform's cards that tenant chooses to display to its own visitors. The
// two were one concept in digitalservice's Package and are separate here —
// which is why assigning a plan to a tenant grants that tenant nothing.
type TenantPlan struct {
	ID       primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	TenantID primitive.ObjectID `bson:"tenant_id" json:"tenant_id"`
	PlanID   primitive.ObjectID `bson:"plan_id" json:"plan_id"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`

	UserID *primitive.ObjectID `bson:"user_id,omitempty" json:"user_id,omitempty"`
}
