# Promote a quote to a tenant (with a notification email) — design

Date: 2026-10-07. Status: design approved in chat; spec awaiting review.
Repos: tenantcore (new route, template, quote field), innonomads/admin (the platform admin console: button and form).

## Goal

Let a platform admin turn a prospect's quote into a tenant from the quotes page in one step, and tell the other platform admins when it happened. Today the only ways to create a tenant are the API (`POST /api/v1/admin/tenants`) or by hand; the admin console has no "New tenant" action and the quotes page can only change a quote's status.

## Background

- A quote (`Quote`) has name, email, phone, company name, requested plan (`plan_slug`), budget, timeline, message and a status of `new`, `contacted`, `quoted` or `closed`. It has an optional `tenant_id`, set only when the lead came through an existing tenant's own storefront. The tenant's Leads page lists quotes by `tenant_id`.
- `TenantService.Create` needs a name and a slug (plus optional contact email and domain), generates the API key, and returns it once. A duplicate slug is a 409.
- tenantcore is the platform's only mail sender. Mail is template-driven: a fixed template with fixed keys, never free text. The subscription-expiry notifier is the pattern for mail sent by tenantcore itself.
- Platform users (`PlatformUser`: name, email, status) have no role field; every platform user is a platform admin.

## Decisions (from the product owner)

- Promote creates the tenant ONLY. The plan and subscription stay a separate step on the tenant's subscription page.
- The notification email carries NO secrets: no API key, no service key, no contact email. A service key belongs to a product service, not to a tenant, and is not created by this flow. The API key is shown once on screen, as it is today.
- Recipients of the notification: all active platform users, one email each.

## Constraints

- Superadmin only (the same guard as the other `/admin` tenant routes). No new public route.
- The tenant API key is never logged, never emailed, never stored in the quote.
- A mail problem never fails or delays the promote. Mail not configured: nothing is sent.
- The notification template has fixed fields, and no field is free text from a visitor (the quote's message is not used).

## 1. tenantcore: `POST /api/v1/admin/quotes/:id/promote`

- Body: `{ "name": string, "slug": string, "contact_email"?: string, "domain"?: string }` (the same validation as tenant creation).
- Response 201: `{ "tenant": <tenant>, "api_key": string, "quote_linked": boolean }`. The key appears in this response only.
- Order of work:
  1. Load the quote; unknown id gives 404.
  2. A quote that already has `promoted_tenant_id` gives 409. (A quote with `tenant_id` set came through an existing tenant's storefront; it is not a prospect and also gives 409 with a clear message.)
  3. Create the tenant through the existing `TenantService.Create` (duplicate slug gives its normal 409; nothing else is changed).
  4. Link the quote: set `promoted_tenant_id` and status `closed` with an update whose filter requires the quote to be not yet promoted, so two admins cannot promote the same quote. If this update fails or matches nothing, still return 201 with `quote_linked: false` and log the quote id and tenant id (never the key). The key is shown once, so it is not hidden behind an error.
  5. Send the notification (below), in the background.
- `Quote.PromotedTenantID *primitive.ObjectID` (`bson:"promoted_tenant_id,omitempty" json:"promoted_tenant_id,omitempty"`). A separate field on purpose: reusing `tenant_id` would list the promoted prospect as a lead of the tenant it became.
- The quote list and detail responses expose `promoted_tenant_id`.
- Narrow interfaces for the service's dependencies (quote store, tenant creator, notifier) so the rules are testable without MongoDB.
- Docs: `docs/api.json` documents the new route (CRLF file: write it back with CRLF), and the route appears wherever `openapi_test.go` and `guard_test.go` require it.

## 2. Notification email

- New template `tenant_promoted` in `pkg/mailer` with required keys `app`, `tenant`, `slug`, `promoted_by`. Subject: `New tenant {{tenant}}`. Body: a short fixed text saying the tenant was created from a quote by `promoted_by`, ending with "Open Tenants in the platform admin to set its plan and subscription." No link and no secrets.
- Recipients: all active platform users (a new narrow `ActiveRecipients` lookup on the platform user repo), one email each, sent in one background goroutine with its own 30 second context. Failures are logged with ids only. An address that fails does not stop the others.
- `promoted_by` is the acting platform user's name. Every value is capped at 256 runes and single-line before it reaches the template.
- If tenantcore's mailer is not configured, or there are no active platform users, nothing is sent.
- Add the template name to `docs/api.json`'s template enum (the openapi test requires the enum to equal `mailer.Names()`).

## 3. Innonomads admin (platform admin console)

- In the expanded quote row (`QuoteRow.tsx`), a quote with no `tenant_id` and no `promoted_tenant_id` shows a **Promote to tenant** button.
- The button opens an inline form prefilled from the quote: name (company name, falling back to the contact name), slug (suggestion), contact email (the quote's email), and an optional domain. All editable.
- Pure helper `suggestSlug(text: string): string` in a plain `.mjs`: lowercase, ASCII letters and digits, runs of anything else become a single hyphen, trimmed, at most 40 characters; empty input gives an empty string.
- Server action `promoteQuoteAction(id, input)` calls the new route. On success the row shows a one-time panel with the API key in a copyable box and the text "This key is shown once. Copy it now.", plus a link to the new tenant's page. The key is held only in the component's state, never in the URL, a cookie or a log, and disappears on navigation or reload. If `quote_linked` is false the panel also says the quote could not be linked and should be closed by hand.
- Errors (duplicate slug, quote already promoted, validation) keep the form open with the message.
- After a promote the row shows "Became tenant X" (name from the tenants list, matched on `promoted_tenant_id`) and status `closed`. The Tenant column keeps showing `tenant_id` as today.
- Types: `Quote.promoted_tenant_id?: string`.

## Out of scope

Creating the subscription or plan, undoing a promote, promoting a quote that came through an existing tenant, a "New tenant" form outside quotes, emailing any key, a link inside the email, and any change to the digitalservice quote flow.

## Verification and what stays unproven

- tenantcore: Go service tests with fakes (success, duplicate slug, already promoted, tenant-linked quote, link failure keeps the key and reports `quote_linked: false`, key never in logs or mail data, notification recipients and failure isolation) and the repo's route and docs guards. Gate: `go build ./... && go vet ./internal/... ./pkg/... && go test ./internal/... ./pkg/... -count=1`.
- Admin: `node --test` for `suggestSlug`, plus `tsc`, build and lint.
- A live promote creates a real tenant in the local `tenantcore_development` database; it needs the owner's go-ahead. Real mail delivery depends on tenantcore's Gmail App Password and is not verified here.

## Merge notes

The `tenant_promoted` template changes `pkg/mailer/template.go` and the `docs/api.json` template enum, which the open `feat/request-notification-template` branch (the request notifier) also changes. Expect a small textual conflict in the enum list.
