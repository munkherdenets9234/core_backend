# Handover

This is the practical, take-it-over-cold companion to `README.md`. README
explains what tenantcore is and why it is shaped the way it is — read that
first for the architecture, the three audiences, the token model and the
entitlement document. This page is about running it, wiring it, and what is
still missing.

## Running it locally

1. Copy `.env.example` to `.env` and fill in the required block. The two that
   are not obvious:
   - `MONGO_URI` / `MONGO_DB` — tenantcore keeps its **own** database,
     separate from every product service, on purpose (see README).
   - `TOKEN_PRIVATE_KEY` — generate it, don't hand-write it:
     ```
     make keygen
     ```
     This prints a fresh Ed25519 keypair. The private half goes in THIS
     service's `.env` as `TOKEN_PRIVATE_KEY` and nowhere else. The public
     half is what you'd paste into a product service's `TOKEN_PUBLIC_KEY` —
     though in practice no product needs that copy-paste today; see
     "Current integration state" below.
2. `make test` — the whole suite that runs on the dev toolchain alone (no
   Docker, no network, no database): route guards, token forgery cases,
   config validation, entitlement rules, and the OpenAPI contract check.
3. `make dev` — runs `go run main.go` against whatever `MONGO_URI` points at.

### Environment variables

From `internal/config/config.go`, which is the source of truth — this list is
transcribed from it, not the other way around:

**Required** (the process refuses to start and reports every missing one at
once, not one restart at a time):

| Variable | Notes |
|---|---|
| `MONGO_URI` | no default — `getEnv` falls back to `mongodb://localhost:27017`, so it's not truly required to boot locally, but `Validate()` still checks it's non-blank |
| `MONGO_DB` | defaults to `tenantcore` |
| `PORT` / `APP_PORT` | defaults to `8090` |
| `TOKEN_PRIVATE_KEY` | no default; `make keygen` generates one |
| `TOKEN_TTL` | defaults to `1h`; must be in `[1m, 24h]` — bounded because tokens are verified offline, so this is how long a suspended platform user's session keeps working |
| `MONGO_CONNECT_TIMEOUT` | defaults to `30s`; bounds the startup connect-and-ping. Raise it for a hosted cluster reached over SRV — the currently-deployed instance uses `60s` because its Atlas link measures ~146ms RTT and one member intermittently fails DNS |

**Optional** (each disables exactly one feature, logged at WARN on startup and
listed on `GET /readyz` for as long as the process runs):

| Variable | Effect when unset |
|---|---|
| `SUPERADMIN_NAME` / `SUPERADMIN_EMAIL` / `SUPERADMIN_PASSWORD` | no first platform user is bootstrapped; see below |
| `PUBLISH_PUBLIC_KEY` (default `true`) | `false` removes `GET /.well-known/tenantcore` entirely — product services must then be configured with the public key by hand |
| `RATE_LIMIT_ENABLED` (default `true`), `AUTH_RATE_PER_MINUTE` (10), `RATE_LIMIT_BURST` (5) | `false` disables login/password-change rate limiting |
| `APP_ENV` | defaults to `development`; anything other than exactly `production` is treated as dev (stack traces on error responses, gin debug logging) |

### Bootstrapping the first superadmin

There's no separate seed command. Set `SUPERADMIN_NAME` / `SUPERADMIN_EMAIL` /
`SUPERADMIN_PASSWORD` and start the process — `PlatformUserService.EnsureBootstrap`
(`internal/service/platform_service.go`) creates that account on startup if no
user with that email exists yet, and never overwrites an existing account's
password on a later restart. Blank the password variable once you have an
account; leaving a fixed password sitting in an env file is not something to
do indefinitely.

There is a separate command, `cmd/migrate-from-digitalservice`, for copying
tenants/platform_users/plans/subscriptions out of digitalservice's database
with every `_id` preserved (`make migrate-dry-run` first). That is the
console cutover path, not day-to-day bootstrapping — see README's "Not done
yet" for its current state (never run against a real database).

## Where it runs today

Per the repo's own `.env` (not committed — read locally, not asserted here
from memory): `APP_PORT=8092`, against a database named `tenantcore` on the
same Atlas cluster the other services share, with `MONGO_CONNECT_TIMEOUT=60s`
for the reason above. This differs from the code's own default port (`8090`,
what `docs/api.json`'s "Local development" server entry uses) — 8092 is a
deployment choice, not the default, presumably to avoid colliding with
another service already using 8090 on the same host. If you're standing up a
new environment, either port works; just make sure whatever value you pick is
what you hand to product services as `TENANTCORE_URL`.

The repo itself: a git repository now exists here (`git status` shows branch
`master`, tracking `origin/master`, one commit — "Initial commit: tenantcore,
the tenant moderator" — clean working tree as of this writing). If you were
told there's no git repo yet, that's stale; check for yourself before
believing either claim.

## The public marketing surface

`/api/v1/public/**` serves the operator's own site: `GET /public/plans`,
`GET /public/projects`, `GET /public/projects/{slug}` and
`POST /public/quotes`. No credential, by design — see README for why that is
narrow rather than a hole, and `docs/api.json` for the shapes.

Two asymmetries in there that look like inconsistencies and are not:

- A **plan** ships its locale maps whole and is resolved in the browser; a
  **project** is resolved server-side from `?lang=`. The price list is small
  and the language toggle must not refetch it; a case study is mostly prose,
  and sending both languages would roughly double the page.
- `PublicPlan` always carries `name.en` and `features.en`, even when a plan
  has no marketing copy at all. Consumers resolve `x[lang] ?? x.en`, so an
  empty map is precisely the case that yields `undefined` and breaks the
  page — the map that looks safest is the one that fails.

## The three credential types

| Credential | Minted by | Verified by | Header |
|---|---|---|---|
| Superadmin bearer token | `POST /api/v1/admin/login` (Ed25519, via `pkg/token.Maker`) | `middleware.Auth.Require`, offline against the public key | `Authorization: Bearer <token>` |
| Service key | `POST /api/v1/admin/service-clients` (shown once) | `middleware.RequireService`, against `service_clients.key_hash` in Mongo | `X-Service-Key` |
| Tenant API key | `POST /api/v1/admin/tenants` or a rotation (shown once) | `EntitlementService.ForAPIKey`, against `tenants.api_key_hash` — only meaningful alongside a valid service key, on `GET /svc/entitlements` | `X-Tenant-Key` |

No credential is required for `POST /api/v1/admin/login`, `/healthz`,
`/readyz`, or `/.well-known/tenantcore`. Full detail, including exactly what
each failure mode returns, is in `docs/api.json`'s `securitySchemes`.

## Current integration state (verified in this repo tree, not assumed)

- **carwash IS wired to tenantcore.** `carwash/internal/entitlement/client.go`
  is an HTTP client implementing the same `Provider` interface described in
  README, calling `GET /api/v1/svc/entitlements` with `X-Service-Key` and
  `X-Tenant-Key`, with a TTL cache and a stale-serving fallback on any
  transport/5xx failure. Configured via `carwash`'s own
  `TENANTCORE_URL` / `TENANTCORE_SERVICE_KEY` (`carwash/internal/config/config.go`).
  This is the live proof that the `svc` surface and the entitlement contract
  work end to end against a real product service.
- **innonomads/admin (the platform console) IS wired to tenantcore.** Its
  `src/lib/api/core.ts`, `src/lib/data/*`, and the tenants/plans/service-clients
  pages under `src/app/(dashboard)` all talk to this service — matching
  README's "Cutover half done" note that the console reads tenants, plans and
  subscriptions from here. `innonomads/admin/CUTOVER.md` documents that side
  if you need the console's own account of it.
- **digitalservice is NOT wired to tenantcore.** No reference to
  `tenantcore`, `TENANTCORE_*`, or `X-Service-Key` exists anywhere in that
  repo's Go source as of this writing. It still reads its own tenants and
  subscriptions at runtime, and tenant-user login still lives there, signed
  with the old shared HMAC secret (see README). Switching digitalservice's
  `entitlement.Provider` to an HTTP client — the same shape carwash already
  uses — is the next concrete step, and is a deliberate, separate act, not
  something this handover assumes is scheduled.

## Known gaps

- **No automated integration suite against a real MongoDB.** Confirmed by
  searching the tree: no test file references `testcontainers`, carries a
  `//go:build integration` tag, or otherwise stands up a live database. The
  repository/index layer, and the unique-constraint behavior in particular
  (two tenants sharing a slug, two subscriptions for one tenant), has only
  been exercised by hand through the console, per README. Everything that
  *can* be tested without a database (route guards, token forgery, config
  validation, entitlement assembly rules, and now the OpenAPI contract) is
  covered by `make test`.
- **Tenant-user login still lives in digitalservice.** tenantcore issues
  superadmin tokens only — there is no tenant-scoped login route here today
  (confirmed: `admin/public` mounts exactly one route, `POST /login`, for
  superadmins; there is no customer- or tenant-facing auth surface at all).
  The claims shape (`user_id`, `role`, `tenant_id`) already matches what
  digitalservice issues, so moving it later is a swap, not a redesign.
- **`PlanRepo.CountSubscribers` has no route.** Written for the console to
  show "N tenants are on this plan" before a delete; nothing calls it yet.
- **No payment provider.** Subscription status and plan are set by hand
  through the console; `UpdatePlan` restarts the billing period on every
  change rather than prorating.
- **`pkg/` is a third copy of code shared with digitalservice and carwash**
  (`apierr`, token/password helpers). It has already drifted once between the
  other two; worth extracting before it drifts a third way here.

(These are transcribed from README's own "Not done yet" section, cross-checked
against the code where checkable — not re-invented here.)

## The API contract

`docs/api.json` (OpenAPI 3.1) is the contract, served live at
`GET /docs/api.json` (unauthenticated — it describes the shape of the API,
not any tenant's data). `docs/docs.go` embeds it into the binary with
`//go:embed`, the same pattern carwash uses.

It is kept honest by `internal/api/openapi_test.go`, which walks the real
route table Gin builds and diffs it against `docs/api.json` in both
directions:

- `TestEveryRouteIsDocumented` fails if the router serves a path the spec
  doesn't mention.
- `TestSpecDocumentsNoRouteThatIsGone` fails if the spec documents a path the
  router no longer serves.
- `TestUndocumentedListIsNotStale` fails if the test's own exemption list
  (currently just `GET /docs/api.json` itself) names a route that doesn't
  exist.

To add a route: implement it, add its path/method to `docs/api.json`, then
`go test ./internal/api/...` — it tells you immediately if you missed one
side or the other. This is a coverage check on paths only; it does not
verify that the documented request/response schema matches the handler, so
review the JSON body by hand against the actual controller when a route's
shape changes.
