# Central tenant resolution in tenantcore — design

Date: 2026-10-05. Status: draft for review. Path: architectural (two services, a data migration, a change to what authenticates every storefront request).

## Purpose

Tenantcore should be the one place that decides who a tenant is: its identity, its API key, its status and its hosts. Product services (digitalservice first) should ask tenantcore instead of keeping their own copy.

Today digitalservice keeps its own `tenants` collection and resolves `X-API-Key` against it, while tenantcore keeps another `tenants` collection. The two drift: rotating a key on the tenantcore Details page changes only tenantcore's hash, and production digitalservice rejected the E&S key held in the operator's env file for that reason (a 401 for a real key and a made-up key alike). Success means: a tenant is created, keyed, rotated, suspended and given a domain in tenantcore only; digitalservice resolves every storefront request through tenantcore; a tenantcore outage degrades to cached answers instead of taking storefronts down; and no tenant's data or isolation changes.

## Decisions already made (2026-10-05)

1. **Availability: serve the last cached answer.** A resolved tenant is cached fresh for about 60 seconds. If tenantcore is unreachable, a stale entry is served for up to 24 hours. A cold cache during an outage still fails.
2. **Source of truth: tenantcore wins, keys re-issued.** Where the two databases disagree about a tenant's key, tenantcore's record is authoritative. A new key is issued in tenantcore and distributed to that tenant's site and admin env files. This costs one short outage per affected tenant.

## What exists already

- Tenantcore `GET /api/v1/svc/entitlements` with `X-Service-Key` and `X-Tenant-Key` already resolves a key to a tenant and returns subscription state. It was built for products that keep no local tenants (carwash uses it).
- Digitalservice's entitlement client (`internal/entitlement/client.go`) already has the cache-plus-grace-window pattern, a `Degraded` flag on `/readyz`, a 3 second timeout, and a service key. Its grace window is 15 minutes.
- The migration tool `cmd/migrate-from-digitalservice` copies tenants to tenantcore at their original `_id`. The tenant `_id` is identical across the two databases by design, which is what keeps tenant data attached to the right tenant.

## Gaps this design closes

1. The entitlement answer carries `tenant_id`, `name`, `slug`, `status`, but not the tenant's **domain**, which digitalservice uses to bind a key to a browser origin (`requestMatchesDomain`). It also reports a suspended tenant as `canceled`, whereas digitalservice treats suspended (403 on everything) differently from cancelled (reads allowed, writes 402). The entitlement route is the wrong shape for identity. It stays as it is.
2. The migration tool upserts tenants verbatim. Run as is, it would overwrite tenantcore's current tenant records (name, status, contact email, key hash) with digitalservice's older copies. It needs an insert-only mode.
3. Nothing stops an anonymous flood of bogus keys. Today that costs one Mongo lookup each; after this change it would cost one HTTP call to tenantcore each. A per-client rate limit must sit in front of resolution.

## Design

### Tenantcore

New route `GET /api/v1/svc/tenants/resolve`, behind `RequireService` like the other `/svc` routes. The key travels in `X-Tenant-Key`, never in the URL. It returns, for the tenant that key belongs to: `tenant_id`, `slug`, `name`, `status` (`active` or `suspended`), `domain`, `hosts`. A suspended tenant answers 200 with `status: "suspended"` so the product can render its own 403. An unknown key answers 401 in the `TENANT` domain, identical for every unknown key. It is documented in `docs/api.json`, which `openapi_test.go` requires, and covered by the existing guard tests (service key required, bearer token refused).

### Digitalservice

A resolver client in `internal/tenantresolve`, modelled on the entitlement client and sharing its base URL and service key configuration. Cache key is the SHA-256 of the raw API key, never the raw key. Fresh TTL 60 seconds. Stale grace 24 hours, served only when tenantcore is unreachable or answers 5xx; `Stale` is surfaced on `/readyz` as degraded. A 401 from tenantcore is cached for 30 seconds (negative cache) so repeated bad keys do not each reach tenantcore. A 24 hour stale window means a suspension or a rotated key can lag by up to 24 hours during an outage; this is the accepted cost of decision 1 and is documented on the type.

`TenantMiddleware.Require` calls the resolver instead of `TenantService.Resolve`. Everything downstream is unchanged: it sets the same `CtxTenantID`, applies the same domain check using the returned `domain`, and a suspended tenant gets the same 403. Tenant admin tokens stay bound to the tenant `_id`, so existing sessions survive and cross-tenant replay is still rejected.

Rollout is controlled by an env setting `TENANT_RESOLVER` with values `local` (today's behaviour, the default) and `tenantcore`. It is a rollout switch, not a runtime fallback: there is no automatic fall back to the local collection when tenantcore fails (decision 1 chose cache over fallback). The local `tenants` collection is kept, unused, until the final phase so the switch can be flipped back.

An IP-keyed rate limit is applied before the resolver on the tenant routes.

### Migration

`cmd/migrate-from-digitalservice` gains an `-only-missing` mode that inserts tenants absent from tenantcore and never touches existing ones. Run with `-dry-run` first, as the tool already supports.

Per tenant:
1. In tenantcore and digitalservice, compare `api_key_last4`. A tenant missing from tenantcore (Inno Nomads, Nomad Trails today) is inserted verbatim by the migration; its existing key keeps working and needs no re-issue.
2. A tenant present in both with different hashes (E&S is expected to be one) is re-issued: rotate in tenantcore, copy the new key once into that tenant's site and admin env files, redeploy, and verify with a public read returning 200.
3. A tenant present in both with the same hash needs nothing.

Tenant data (tours, bookings, translations, users) is not moved. It is keyed by tenant `_id`, which does not change.

### Phases

1. Tenantcore resolve route, migration `-only-missing`, digitalservice resolver behind `TENANT_RESOLVER=local`. Ships with no behaviour change.
2. Migrate and re-issue, one tenant at a time, then set `TENANT_RESOLVER=tenantcore` on production digitalservice and watch `/readyz` for degraded and the error rate.
3. After a stable period, remove digitalservice's duplicate management routes (create, rotate key, status, domain) and the local lookup. Not part of the first plan.

## Error handling

- Tenantcore 401 for a key: digitalservice answers 401, same body as today.
- Suspended: 403, same as today.
- Tenantcore unreachable, cache fresh or within grace: serve the cached identity, mark `/readyz` degraded.
- Tenantcore unreachable, no usable cache: 503 (not 401, so a caller can tell an outage from a bad key).
- Tenantcore answers 5xx or malformed JSON: treated as unreachable.

## Testing

- Go unit tests in both services with an `httptest` fake tenantcore. Cases: fresh hit, expiry and refetch, stale serve on outage, stale expiry past 24 hours, negative cache, suspended, domain mismatch, no cache plus outage gives 503, a rotated key is rejected after the TTL, the raw key never appears in logs or cache keys. Mutation-check the stale and negative-cache rules.
- Tenantcore: resolve route returns 401 for an unknown key, 200 `suspended` for a suspended tenant, refuses a bearer token, and appears in the guard and openapi tests.
- Migration tool: `-only-missing` leaves an existing tenant untouched (tested against a fake or a recorded dry run; there is no Docker here).
- Live verification only with throwaway data in a non-shared database or with explicit approval for each write to the shared Atlas cluster. Never rotate a live tenant's key as a test.

## Out of scope

Carwash (already resolves through tenantcore). The three known PII exposures other than the flood protection above. Moving tenant data into tenantcore. Removing digitalservice's management routes (phase 3). A runtime fallback to the local collection.

## Risks to watch

- **Availability and cold starts.** Tenantcore on Render's free tier cold-starts slowly; a cold cache plus a cold tenantcore fails requests. A minimum instance or a keep-warm ping for tenantcore is worth deciding separately.
- **Key re-issue coordination.** Each re-issued tenant breaks until both its site and admin envs are updated and redeployed. Put the right key in the right project, or one tenant serves another's data.
- **Same `_id` assumption.** If a tenant's `_id` differs between databases, its data is orphaned. The dry run must report any mismatch before anything is switched.
- **24 hour stale window.** Long enough that an outage could mask a suspension. If that is too long, shorten the window; the number lives in one constant.
