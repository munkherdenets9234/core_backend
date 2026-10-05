# tenantcore

The platform's tenant moderator. It owns what every product service must
agree on — who a tenant is, who the platform's own staff are, what each
tenant has bought — and answers one question for the products:

> What is this tenant allowed to run?

It does not know what any product sells. Adding a product is a new module
name in a plan, not a change here.

## The platform principle

**Tenantcore is responsible for all tenant-related information and
management.** That means a tenant's identity (name, slug, contact), its API
key (issue, rotate, revoke), its status (active or suspended), its domain and
hosts, its plan and its subscription. These are created, changed and read
here and nowhere else.

**A product service does not manage tenants. It provides its service to a
tenant that tenantcore has already identified.** A product holds one
credential of its own (a service key) and, on each request, asks tenantcore
who the caller's API key belongs to and whether that tenant may proceed. It
keeps only what it sells (its content, bookings, users), keyed by the tenant's
`_id`, which is the same value everywhere.

This is the direction of travel, not yet the whole of today's state:
digitalservice still keeps a duplicate `tenants` collection and resolves
`X-API-Key` against it, which is how the two copies drifted (see
`docs/superpowers/specs/2026-10-05-central-tenant-resolution-design.md`).
Carwash already resolves through tenantcore. New products should follow the
carwash pattern from day one: no local tenants collection, no tenant
management routes.

## What it owns

| | |
|---|---|
| `tenants` | identity, API key, domain binding, status |
| `platform_users` | the operator's own staff; tenantcore issues their tokens |
| `service_clients` | the product services permitted to call it |
| `plans` | price, period, what subscribing **grants**, and the pricing card that advertises it |
| `subscriptions` | one per tenant |
| `tenant_details` | the operator's case studies about tenants it has onboarded |
| `quotes` | leads from the operator's contact form |
| `tenant_plans` | which pricing cards a tenant displays on its own storefront |

The last three are the operator's own marketing content, not a product's.
That is the line: Inno Nomads showing prospects who it has onboarded, and
collecting enquiries from people who are not tenants of anything yet. A
tenant's own storefront content still belongs to the product serving it.

What it deliberately does not own: anything a product sells.

## The four audiences

```
/api/v1/admin/login          public   — no credential
/api/v1/admin/**             console  — superadmin bearer token
/api/v1/public/**            visitor  — no credential; the operator's own site
/api/v1/svc/**               machine  — X-Service-Key, one per product service
/healthz /readyz /.well-known/tenantcore /docs/api.json   open
```

`/public` is a narrow, deliberate exception to the rule that everything here
needs a credential. Three things keep it narrow: every read returns only what
has been explicitly published (an active plan, a showcased case study), and
the filter lives in the repository query rather than a handler that could
forget it; no view it can reach has a field for a contact email, an API key
fragment, a tenant status or an entitlement; and its one write — the contact
form — is rate limited and cannot set its own status. `internal/api/guard_test.go`
lists those four routes explicitly, so a fifth has to be argued for in review.

Each is a separate package with its own `Register`, mounted on a group that
carries its own middleware. A controller in `admin/private` cannot be reached
without a token no matter how its file is edited, because the gate is on the
group rather than in the handler.

The console credential and the service credential are separate on purpose. A
superadmin token does not open `/svc`, and a service key does not open the
console — both are asserted in `internal/api/guard_test.go`.

## Tokens are asymmetric, and that is the point

tenantcore signs with **Ed25519**. Product services hold only the public key:
they can verify a token and cannot forge one.

With a shared HMAC secret — which is what the product services use among
themselves today — the signing key and the verifying key are the same string.
Any service able to check a token is able to mint one, including a
platform-superadmin token. A compromised car wash deployment would be able to
issue itself the keys to the kingdom.

```
make keygen
```

Private half → this service's `TOKEN_PRIVATE_KEY`, nowhere else.
Public half → every product service, or fetched from
`GET /.well-known/tenantcore`.

Tokens are verified **offline**, so suspending a platform user does not bite
until their current token expires. `TOKEN_TTL` is that window, bounded to 24h.

## The entitlement document

What `/api/v1/svc/entitlements` returns, and the only thing a product service
ever receives:

```json
{
  "tenant_id": "...",
  "status": "active",
  "period_end": "2026-10-22T00:00:00Z",
  "modules":  ["travel", "carwash"],
  "limits":   {"locations": 3, "staff": 10},
  "features": {"custom_domain": true},
  "stale": false
}
```

Two axes, deliberately separate. `modules` is **which products**; `limits` and
`features` are the **business level** within a product the tenant already has.
Collapsing them looks tidy until you need "has carwash, but only three
branches" and start encoding the tier into the module name.

Three rules hold it together:

1. **A lookup failure is "could not find out", never "no".** A consumer that
   turned an unreachable tenantcore into a denial would make this service a
   single point of failure for every product at once — worse than the
   monolith it replaces. Serve last-known state and set `stale`.
2. **The platform stores the number; the product decides what it means.**
   tenantcore does not know that `locations` counts car wash branches, and
   must not learn: the moment billing understands product semantics, every
   new feature needs a coordinated two-repo deploy.
3. **Add fields, never rename them.** Producer and consumer are separate
   copies of one wire contract. An older side reads a renamed field as a zero
   value — which for `modules` means *unenforced*.

Three states are **not** errors, because a product must keep serving through
all of them: a tenant with no subscription yet, a plan deleted out from under
a live subscription, and a suspended tenant (reported as a status).

## Connecting a product service

1. `POST /api/v1/admin/service-clients` → returns a key, once.
2. Put it in that service's `TENANTCORE_SERVICE_KEY`.
3. Point its entitlement client at `/api/v1/svc/entitlements`.

A product that keeps no tenants collection of its own can resolve a tenant's
key to its identity (id, slug, name, status, domain, hosts) with
`GET /api/v1/svc/tenants/resolve`, sending the key in `X-Tenant-Key`. An unknown
key is a 401; a suspended tenant resolves with `status: suspended`.

In digitalservice that last step is replacing one implementation of
`entitlement.Provider` — the interface already exists and every call site is
written against it, so nothing else changes.

## Running it

```
make keygen          # once; put the private half in .env
make test            # no Docker, no database, no network
make dev
```

`make test` is the suite that runs on the dev toolchain alone. There is still
no automated integration suite, but the service has now been run against a
real MongoDB: indexes are created, the superadmin bootstrap works, and the
full path — create a plan, create a tenant, subscribe it, and have a product
service fetch the entitlement with its own key — has been exercised by hand
through the platform console.

## Not done yet

- **No automated integration suite.** The service has been run against a real
  MongoDB and the green path works, but nothing re-checks it. The unique
  constraints in particular have only been exercised in the happy direction —
  no test tries to create two subscriptions for one tenant, or two tenants
  with one slug, and finds out what the API does about it.
- **Cutover half done.** The platform console
  (`innonomads/admin`) reads tenants, plans and subscriptions from here, and
  `cmd/migrate-from-digitalservice` copies the data across with every `_id`
  preserved (`make migrate-dry-run` first). What has NOT happened: the
  migration has never been run against a real database, and digitalservice
  still reads its own tenants and subscriptions at runtime — switching its
  `entitlement.Provider` to an HTTP client is still a separate, deliberate
  step. Until it is, the two copies of the tenant collection drift the moment
  either side is written to, and the console writes to this one.
- **Tenant-user login still lives in digitalservice**, signed with the old
  shared HMAC secret. tenantcore issues superadmin tokens only. The claims
  shape here already matches, so moving it later does not change any
  consumer. The console works around it by signing in to BOTH services with
  one form and carrying two tokens; that second half disappears when
  digitalservice learns to verify this service's public key.
- **`PlanRepo.CountSubscribers` has no route.** It was written so the console
  could say "4 tenants are on this plan" before offering the delete button,
  and there is nothing to call. Deleting a plan does not just orphan its
  subscribers — it empties their modules and limits, which reads as a
  downgrade and is the opposite: an empty module list means the gate is not
  enforced and an absent limit means unlimited. The console warns in words;
  a count would let it warn in numbers.
- **No payment provider.** Subscription status and plan are set by hand.
  `UpdatePlan` restarts the billing period on every change, which is wrong
  for a mid-period upgrade and is the first thing to fix when proration
  becomes meaningful.
- **`pkg/` is a third copy** of the core shared with digitalservice and
  carwash. `apierr` has already drifted between the two existing copies.
  Extracting a shared module is worth doing before it drifts further.
