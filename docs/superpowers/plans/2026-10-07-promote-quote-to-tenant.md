# Promote Quote To Tenant Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A platform admin promotes a prospect's quote to a tenant from the quotes page in one step (tenant only), the API key is shown once, the quote is linked and closed, and the other platform admins get a no-secrets notification email.

**Architecture:** tenantcore gets `POST /api/v1/admin/quotes/:id/promote` backed by a `PromoteService` that uses narrow interfaces (quote store, tenant creator, notifier). The link uses a new `promoted_tenant_id` field and a conditional update. A fixed-field `tenant_promoted` template is mailed in the background to every active platform user. The Innonomads admin gets a Promote button, an inline form prefilled from the quote, and a one-time key panel.

**Tech Stack:** Go (gin, mongo-driver) in `tenantcore`; Next.js server actions and plain `.mjs` helpers (tested with `node --test`) in `innonomads/admin`.

**Spec:** `tenantcore/docs/superpowers/specs/2026-10-07-promote-quote-to-tenant-design.md`

## Global Constraints

- Superadmin only: the route lives in the same authenticated group as the other `/admin/quotes` and `/admin/tenants` routes. No new public route.
- The tenant API key is returned ONLY in the promote response. It is never logged, never put in mail data, never stored on the quote, never in a URL, cookie or any error message.
- `POST .../promote` body `{name, slug, contact_email?, domain?}` follows the existing tenant-creation validation and mapping (reuse `TenantService.Create`; do not duplicate its rules). Response 201 `{ "tenant": <tenant>, "api_key": string, "quote_linked": boolean }`.
- Errors: unknown quote id 404; invalid id 400; quote already has `promoted_tenant_id` 409; quote with `tenant_id` set (it came through an existing tenant's storefront) 409 with a clear message; duplicate slug 409 (the existing tenant error); validation 400/422 as the tenant creation does.
- Link step: set `promoted_tenant_id`, status `closed` and `user_id` with an update whose filter requires `promoted_tenant_id` to be absent (so two admins cannot promote the same quote). If it fails or matches nothing after the tenant exists, still return 201 with `quote_linked: false`, log the quote id and tenant id only.
- `Quote.PromotedTenantID *primitive.ObjectID` (`bson:"promoted_tenant_id,omitempty" json:"promoted_tenant_id,omitempty"`), a separate field from `tenant_id` (which means "lead came through that tenant's storefront" and drives the tenant's Leads page).
- Notification: template `tenant_promoted`, required keys `app`, `tenant`, `slug`, `promoted_by`; subject `New tenant {{tenant}}`; fixed body ending "Open Tenants in the platform admin to set its plan and subscription."; no link, no secrets, no contact email, no quote text. Recipients: all active platform users, one email each, sent in ONE background goroutine with its own 30 s context after a successful promote; one failing address never stops the others; failures are logged with ids only; mail not configured or no active users sends nothing; a mail problem never fails or delays the promote. Every value is single-line and capped at 256 runes before it reaches the template.
- `docs/api.json` is CRLF: write it back with CRLF (a LF rewrite makes the whole file look changed). It must document the new route and list `tenant_promoted` in the template enum (`openapi_test.go` requires the enum to equal `mailer.Names()`; `guard_test.go` sweeps routes).
- Admin: the API key lives only in React component state; it disappears on navigation or reload; it is never in a URL, cookie, localStorage, console or server log. Pure helpers in plain `.mjs` with `.d.mts` typings and `node --test`. No new dependencies in any repo.
- AGENTS.md in each repo applies (no secrets in files or logs, no `--no-verify`, nothing pushed, `.env*` values never printed, Go services: `logger.Init`-built loggers, tenant-scoped queries, generic client errors).
- Go gate (tenantcore): `go build ./... && go vet ./internal/... ./pkg/... && go test ./internal/... ./pkg/... -count=1`; never `go test ./...`. Admin: `node --test src/lib/*.test.mjs`, `npx tsc --noEmit`, `npm run build` (use `next build --webpack` if the `node_modules` junction trips Turbopack), `npm run lint` (report existing errors separately).
- Worktrees (never the user's own checkouts): tenantcore `D:\bkup\projects\digitalbrochure\tenantcore-promote` (branch `feat/promote-quote`, exists); admin `D:\bkup\projects\digitalbrochure\innonomads-admin-promote` (branch `feat/promote-quote` from `master` of `innonomads/admin`, created in Task 5; junction `node_modules` to the main checkout's).

## Review Focus

- Two concurrent promotes of one quote: exactly one links, the other gets 409 or `quote_linked: false` (never two tenants silently linked to one quote), and the key is never lost when the link step fails.
- The key never appears in any log line, mail data, error text, response other than the 201, or admin URL; the notifier is given the tenant name, slug and actor name only.
- A quote that already has `tenant_id` (an existing tenant's lead) cannot be promoted; the tenant's Leads page (`tenant_id` listing) is unchanged by a promote.
- Notification: a failing address does not block the others; mail off or zero recipients sends nothing; a send error never changes the promote result; `promoted_by` with CR/LF or 10,000 characters is single-line and capped.
- Slug suggestion: empty, non-ASCII (Mongolian, Korean, accents), punctuation only, very long and leading/trailing hyphen inputs never produce an invalid slug or throw.
- A staff or anonymous caller is refused on the new route; the route is in `docs/api.json` and the guards stay green.

---

### Task 1: tenantcore template `tenant_promoted`

**Files (tenantcore-promote):**
- Modify: `pkg/mailer/template.go`, `docs/api.json` (template enum and description only)
- Test: `pkg/mailer/template_test.go`

**Interfaces:**
- Produces: `mailer.TemplatePromoted Template = "tenant_promoted"` with required keys `app`, `tenant`, `slug`, `promoted_by`; subject `New tenant {{tenant}}`.

- [ ] **Step 1: Write failing tests:** `TestTenantPromotedRenders` (all four keys; subject has the tenant; body has slug, promoted_by and the fixed closing sentence; `Known("tenant_promoted")` true); `TestTenantPromotedRequiresAllKeys` (drop each key, error names it); `TestTenantPromotedSubjectCannotCarryLineBreaks` (`tenant` = `x\r\nBcc: a@b.c` gives a single-line subject); `TestTenantPromotedBodyHasNoKeyOrLink` (the rendered body and subject contain no `http` substring and no `api_key`).
- [ ] **Step 2: Run** `go test ./pkg/mailer -run TenantPromoted -v`. Expected: FAIL, undefined `TemplatePromoted`.
- [ ] **Step 3: Implement** the const and `templates` entry modelled on `TemplateSubscriptionExpiring`; add `tenant_promoted` to the `docs/api.json` template enum (sorted position, CRLF preserved, only a few diff lines).
- [ ] **Step 4: Run** the Go gate. Expected: PASS including `openapi_test.go`.
- [ ] **Step 5: Commit** touched files, message `feat(mailer): tenant_promoted template`.

---

### Task 2: Quote link field and repository methods

**Files (tenantcore-promote):**
- Modify: `internal/models/content.go` (`Quote.PromotedTenantID`), `internal/repository/content_repo.go` (`QuoteRepo`), `internal/repository/platform_repo.go` (`PlatformUserRepo`)
- Test: `internal/repository/content_repo_test.go` and `platform_repo_test.go` following the pure filter/update builder pattern used by other repository tests (read `internal/repository/*_test.go` first; no live MongoDB in this environment)

**Interfaces:**
- Produces: `(*QuoteRepo).FindByID(ctx, id primitive.ObjectID) (*models.Quote, error)` returning `mongo.ErrNoDocuments` when absent; `(*QuoteRepo).LinkPromoted(ctx context.Context, id, tenantID primitive.ObjectID, userID *primitive.ObjectID) (linked bool, err error)` doing one conditional `UpdateOne` with filter `{_id: id, promoted_tenant_id: {$exists: false}}` setting `promoted_tenant_id`, `status: closed`, `user_id` (when given) and `updated_at`, returning `linked == MatchedCount > 0`; `(*PlatformUserRepo).ListActive(ctx context.Context) ([]*models.PlatformUser, error)` (status active only); pure builders `promoteLinkFilter(id)` and `promoteLinkUpdate(tenantID, userID, now)`.

- [ ] **Step 1: Write failing tests** on the pure builders: `TestPromoteLinkFilterRequiresUnpromoted` (exact `reflect.DeepEqual` filter), `TestPromoteLinkUpdateSetsLinkStatusAndActor` (closed status, `promoted_tenant_id`, `user_id` present only when given, `updated_at` set), `TestListActiveFilterIsActiveOnly`.
- [ ] **Step 2: Run** `go test ./internal/repository -run 'Promote|ListActive' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the field, the builders and the three repo methods. The quote list and detail JSON already serialize the struct, so `promoted_tenant_id` is exposed with no extra code.
- [ ] **Step 4: Run** the Go gate. Expected: PASS.
- [ ] **Step 5: Commit** touched files, message `feat(quotes): promoted_tenant_id link and platform user listing`.

---

### Task 3: `PromoteService` and the notifier

**Files (tenantcore-promote):**
- Create: `internal/service/promote_service.go`
- Test: `internal/service/promote_service_test.go`

**Interfaces:**
- Consumes: Task 2 repo methods; `TenantService.Create(ctx, t *models.Tenant) (*models.Tenant, string, error)`; the mailer `sender` interface used by `ExpiryNotifier` (read `internal/service/expiry_notifier_service.go` for its exact shape); `mailer.TemplatePromoted` (Task 1).
- Produces:
  - narrow interfaces `promoteQuotes { FindByID(ctx, id) (*models.Quote, error); LinkPromoted(ctx, id, tenantID primitive.ObjectID, userID *primitive.ObjectID) (bool, error) }`, `promoteTenants { Create(ctx, t *models.Tenant) (*models.Tenant, string, error) }`, `promoteRecipients { ListActive(ctx) ([]*models.PlatformUser, error) }`
  - `type PromoteInput struct { Name, Slug, ContactEmail, Domain string }`, `type PromoteResult struct { Tenant *models.Tenant; APIKey string; QuoteLinked bool }`
  - `func NewPromoteService(quotes promoteQuotes, tenants promoteTenants, users promoteRecipients, mail sender, appName string, log *zap.Logger) *PromoteService` (nil `mail` is allowed: no notification)
  - `func (s *PromoteService) Promote(ctx context.Context, quoteID string, in PromoteInput, actorID *primitive.ObjectID, actorName string) (*PromoteResult, error)`
  - `func (s *PromoteService) Drain()` waits for in-flight notification goroutines (used by tests and shutdown)

- [ ] **Step 1: Write failing tests with fakes:** `TestPromoteCreatesTenantLinksQuoteAndReturnsKey`; `TestPromoteUnknownQuoteIs404` (no tenant created); `TestPromoteInvalidIDIs400`; `TestPromoteAlreadyPromotedIs409` (no tenant created); `TestPromoteQuoteWithTenantIDIs409` (no tenant created); `TestPromoteDuplicateSlugPropagatesAndDoesNotLink` (fake tenants returns the 409; `LinkPromoted` not called; no mail); `TestPromoteLinkFailureStillReturnsKeyWithQuoteLinkedFalse` (link returns error, and a second case returns `false`; result carries the key, no error, no mail suppression); `TestKeyNeverInLogsOrMailData` (zap observer and the fake sender capture: the raw key string appears in neither); `TestNotifySendsOneMailPerActiveUser` (template `tenant_promoted`, data keys exactly `app, tenant, slug, promoted_by`); `TestNotifyFailureOfOneAddressDoesNotBlockOthers`; `TestNoMailerOrNoRecipientsSendsNothing`; `TestPromotedByIsSingleLineAndCapped` (CR/LF and 10,000 characters in the actor name or tenant name); `TestMailFailureDoesNotChangeResult`.
- [ ] **Step 2: Run** `go test ./internal/service -run Promote -v`. Expected: FAIL, undefined `NewPromoteService`.
- [ ] **Step 3: Implement** `Promote`: parse the quote id (`apierr.BadRequest("invalid quote id")`); `FindByID` mapping `mongo.ErrNoDocuments` to `apierr.NotFound("quote")`; reject `PromotedTenantID != nil` and `TenantID != nil` with 409 `apierr.Conflict(...)` messages; build a `models.Tenant` from the input exactly as the existing `tenantsController.Create` does (read `internal/api/admin/private/tenants.go` and mirror its field mapping and domain handling, do not re-derive it); call `tenants.Create`; call `LinkPromoted`, treating `(false, nil)` and any error as `QuoteLinked=false` with an id-only log; then start the notification goroutine (own `context.WithTimeout(context.Background(), 30*time.Second)`, `sync.WaitGroup` for `Drain`, recipients from `ListActive`, one `mail.Send` per user, per-send errors logged with the quote id and user id only). Collapse whitespace and cap by runes (256) every template value.
- [ ] **Step 4: Run** the Go gate. Expected: PASS.
- [ ] **Step 5: Commit** both files, message `feat(quotes): PromoteService creates a tenant from a quote and notifies admins`.

---

### Task 4: Route, wiring and docs

**Files (tenantcore-promote):**
- Create: `internal/api/admin/private/promote.go`
- Modify: `internal/api/admin/private/private.go` (route + `Deps`), `internal/api/router.go` and `internal/bootstrap/bootstrap.go` (build the service from the existing quote repo, tenant service, platform user repo, mailer and logger; `Drain` in `App.Close` next to the expiry notifier's shutdown), `docs/api.json`
- Test: `internal/api/admin/private/promote_test.go`; extend `internal/api/openapi_test.go` / `guard_test.go` only if they need the new route listed

**Interfaces:**
- Consumes: `PromoteService.Promote`, `Drain` (Task 3); `apictx.ActorID(c)` and the platform user lookup used by other controllers to resolve the acting admin's display name.
- Produces: route `POST /admin/quotes/:id/promote` on the existing `q` group (same auth as the sibling quote routes), controller binding `{name, slug, contact_email?, domain?}` (name and slug required) and answering 201 with `{tenant, api_key, quote_linked}` via the repo's response helper; the tenant is rendered with the same view the tenant create route uses so the key hash is never exposed.

- [ ] **Step 1: Write failing tests:** `TestPromoteRouteRequiresSuperadmin` (anonymous 401; a non-superadmin token 403, using the real auth middleware like sibling private tests); `TestPromoteReturns201WithKeyOnce` (response has `api_key`, `tenant`, `quote_linked`; the tenant object has no key hash); `TestPromoteMapsServiceErrors` (404, 409 already promoted, 409 duplicate slug, 400 invalid id and missing fields); `TestPromoteBodyRejectsUnknownOrBadTypes`; update/extend the route-table and docs guard tests so the new route is expected and documented.
- [ ] **Step 2: Run** `go test ./internal/api/... -run Promote -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the controller, route, wiring and shutdown drain. Document the route in `docs/api.json` (CRLF): request body, 201 response, 401/403/404/409 errors.
- [ ] **Step 4: Run** the Go gate. Expected: PASS including `openapi_test.go` and `guard_test.go`.
- [ ] **Step 5: Commit** touched files, message `feat(quotes): POST /admin/quotes/:id/promote`.

---

### Task 5: Admin console button, form and one-time key panel

**Files (admin worktree `innonomads-admin-promote`):**
- Create: `src/lib/slug.mjs`, `src/lib/slug.d.mts`, `src/lib/slug.test.mjs`, `src/components/PromoteQuoteForm.tsx`
- Modify: `src/components/QuoteRow.tsx`, `src/app/admin/(console)/quotes/actions.ts`, `src/app/admin/(console)/quotes/page.tsx`, `src/lib/types.ts`

**Interfaces:**
- Produces: `suggestSlug(text: string): string` (lowercase, ASCII letters and digits only, any run of other characters becomes one hyphen, trimmed of leading/trailing hyphens, at most 40 characters, `''` for empty or symbol-only input, never throws); `Quote.promoted_tenant_id?: string`; server action `promoteQuoteAction(id: string, input: { name: string; slug: string; contact_email?: string; domain?: string }): Promise<{ ok: true; tenant: { id: string; name: string; slug: string }; apiKey: string; quoteLinked: boolean } | { ok: false; error: string }>` (never throws to the client; the error text is the backend's message for 4xx and a generic message otherwise; the key is returned to the caller only in the result and never logged or revalidated into a page); `<PromoteQuoteForm quote tenantNames />`.
- Consumes: `POST /admin/quotes/:id/promote` (Task 4); existing `apiPost`, `requireToken`, `ApiError` from the admin client.

- [ ] **Step 0: Create the worktree** from `innonomads/admin`: `git worktree add -b feat/promote-quote ../../innonomads-admin-promote master` (adjust the relative path so the worktree is `D:\bkup\projects\digitalbrochure\innonomads-admin-promote`); junction `node_modules`.
- [ ] **Step 1: Write failing tests** `slug.test.mjs`: `"Acme Travel LLC"` gives `acme-travel-llc`; `"  --Hello__World!! "` gives `hello-world`; `"Монгол Аялал"` and `"서울 투어"` (no ASCII) give `''`; `"Café Münch"` gives `caf-m-nch`-style ASCII-only output with single hyphens (assert no non-`[a-z0-9-]` characters and no `--`); `""` and `"!!!"` give `''`; a 200-character input is cut to at most 40 characters with no trailing hyphen; never throws for `undefined`/`null`/numbers (returns `''`).
- [ ] **Step 2: Run** `node --test src/lib/slug.test.mjs`. Expected: FAIL, module not found.
- [ ] **Step 3: Implement** `slug.mjs` (+ typings); run the test again. Expected: PASS.
- [ ] **Step 4: Implement the action and UI.** `promoteQuoteAction` posts to the new route with the admin token and maps `ApiError` as above. `QuoteRow`: in the expanded panel a quote with no `tenant_id` and no `promoted_tenant_id` shows a "Promote to tenant" button that reveals `PromoteQuoteForm` (name from `company_name` falling back to `name`, slug from `suggestSlug(name)` re-suggested only until the user edits it, `contact_email` from the quote's email, optional domain, Cancel). On success the component keeps the API key in `useState` ONLY and renders a panel: "This key is shown once. Copy it now.", a read-only field with a Copy button (clipboard API with a visible fallback), a link to `/admin/tenants/<id>`, and, when `quoteLinked` is false, a notice that the quote could not be linked and should be closed by hand; a "Done" button clears the state. Errors keep the form open with the message. A promoted quote row shows "Became tenant <name>" (name looked up from the tenants list passed by `page.tsx` by `promoted_tenant_id`) and the `closed` status; the Tenant column keeps its current `tenant_id` meaning. Do not put the key in any URL, cookie, storage, `revalidatePath` argument or log.
- [ ] **Step 5: Verify** `node --test src/lib/*.test.mjs`, `npx tsc --noEmit`, `npm run build` (or `npx next build --webpack`), `npm run lint`. Expected: no new errors.
- [ ] **Step 6: Commit** touched files in the admin worktree, message `feat(admin): promote a quote to a tenant from the quotes page`.

---

## Final verification (controller, after all tasks)

Run tenantcore from `tenantcore-promote` (copy `.env` as before, never printed) and the admin from the new worktree. Without creating data, confirm: the admin quotes page loads; a prospect quote shows the Promote button and the prefilled form; the slug suggestion behaves; a promote of an unknown id and a validation error show clean messages. A real promote creates a tenant in `tenantcore_development` and needs the owner's explicit go-ahead first. Mail delivery depends on the Gmail App Password and stays unverified.

## Self-review notes

- Spec coverage: §1 route and link -> Tasks 2, 3, 4; §2 notification -> Tasks 1, 3; §3 admin -> Task 5; "Merge notes" (enum conflict with `feat/request-notification-template`) -> reported at the end, not code.
- The spec names the quote's plan field `plan_slug`; tenantcore's model field is `package_slug` (the admin type calls it `plan_slug`). Promote does not read it, so no task depends on the name.
