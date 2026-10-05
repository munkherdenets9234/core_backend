# Runbook: central tenant resolution rollout

Moves digitalservice from resolving `X-API-Key` against its own `tenants`
collection to asking tenantcore (`TENANT_RESOLVER=tenantcore`). Design:
`docs/superpowers/specs/2026-10-05-central-tenant-resolution-design.md`.

Status as of 2026-10-05: code done and committed in both repos; rollout NOT
started; `TENANT_RESOLVER` still defaults to `local`.

This file contains no secrets and must never gain any. Placeholders such as
`$MONGO_URI_SOURCE` are read from the operator's own shell environment.

## Never

- Never rotate a live tenant's key to test something.
- Never paste keys, tokens, hashes or connection strings into chat, tickets,
  commits or logs.
- Never run the migration without `-dry-run` first.
- Never run the migration against the live tenantcore database without
  `-only-missing`.
- No assistant or automation performs any step marked **REQUIRES THE OWNER'S
  EXPLICIT APPROVAL AT THE TIME**. The owner runs it, or approves that single
  action in the moment. Earlier approval does not carry over.

## Rollback caveat (read before step 4)

After a tenant's key is re-issued in tenantcore, setting `TENANT_RESOLVER`
back to `local` does NOT restore that tenant. digitalservice's local `tenants`
collection still holds the OLD key hash, and no tool copies tenantcore's hash
back: the migration copies digitalservice to tenantcore only. Recovery for
that tenant is a digitalservice-side key rotate (digitalservice's own rotate
route), then updating that tenant's env files and redeploying. Tenants that
were not re-issued roll back cleanly.

## Step 1. Preconditions

- tenantcore production is reachable and its mail sender works.
- digitalservice production has `TENANTCORE_URL` and `TENANTCORE_SERVICE_KEY`
  set, and the service client behind that key is active in tenantcore.
- The new digitalservice and tenantcore code is deployed, with
  `TENANT_RESOLVER` unset (so `local`).

Read-only checks (placeholders, no credentials):

```
curl -fsS "$TENANTCORE_URL/healthz"
curl -fsS "$TENANTCORE_URL/readyz"
curl -fsS "$DIGITALSERVICE_URL/healthz"
curl -fsS "$DIGITALSERVICE_URL/readyz"
```

Success: all four answer 200. digitalservice `/readyz` lists `entitlement`
with `enabled: true`, has no `tenant_resolver_tenantcore` feature (still
local) and no `tenant_resolver` block. If tenantcore is on a cold-start host,
repeat the first call until it answers.

## Step 2. Dry run (read-only on both databases, writes nothing)

From the tenantcore repo, URIs from your own environment:

```
go run ./cmd/migrate-from-digitalservice \
  -from "$MONGO_URI_SOURCE" -from-db "$MONGO_DB_SOURCE" \
  -to   "$MONGO_URI_TARGET" -to-db   "$MONGO_DB_TARGET" \
  -only-missing -dry-run
```

(`-from`/`-to` default to `DIGITALSERVICE_MONGO_URI` / `MONGO_URI`.) The tool
prints the source and destination URIs in its first log line; do not paste
that output anywhere unredacted.

Read:
- per collection `would:` summary lines (inserted and skipped counts) for
  tenants, platform_users, plans, subscriptions;
- the tenants key-mismatch report: id, name and the last four characters of
  each side's hash. These are the tenants needing re-issue (step 4).

Success: the run ends with `dry run complete`, and the counts match what you
expect (tenants absent from tenantcore, today Inno Nomads and Nomad Trails,
are the ones that would be inserted).

STOP if any tenant's `_id` differs between the two databases: its data would
be orphaned. Resolve by hand with the owner before going on.

## Step 3. Insert missing tenants (writes to the shared database)

**REQUIRES THE OWNER'S EXPLICIT APPROVAL AT THE TIME. The assistant or
automation must not run this.**

The same command without `-dry-run`, keeping `-only-missing`:

```
go run ./cmd/migrate-from-digitalservice \
  -from "$MONGO_URI_SOURCE" -from-db "$MONGO_DB_SOURCE" \
  -to   "$MONGO_URI_TARGET" -to-db   "$MONGO_DB_TARGET" \
  -only-missing
```

Inserts use `$setOnInsert`: documents already in tenantcore are never
touched. Tenants newly inserted keep their existing keys; no re-issue, no env
change.

Success: log ends with `done.`; the inserted counts equal the dry run's
`would:` counts; skipped counts cover the rest. Open the tenants list in the
platform console and confirm the inserted tenants appear.
Rerunning is safe (insert-only).

## Step 4. Re-issue mismatched tenants, one at a time

Applies to the tenants listed by the mismatch report (E&S expected). Their
old key stops working in tenantcore-resolved mode at the moment of rotation,
and E&S's subscription ends 2026-10-20: do not do this in the last days of
that period, and finish the flip well before it. Re-read the rollback caveat
above first.

**REQUIRES THE OWNER'S EXPLICIT APPROVAL AT THE TIME, per tenant. The
assistant or automation must not rotate a key or edit production env files.**

Order for each tenant, finishing one before starting the next:

1. Rotate the key in tenantcore (console, tenant Details page). The new key
   is shown once; copy it straight into step 2 and nowhere else.
2. Set `TENANT_API_KEY` in that tenant's site env file and its admin env file
   (hosting dashboard, not a file in git), and redeploy both.
3. Verify the public translations read (or any public read through the
   site) returns 200 for that tenant.
4. Only then move to the next tenant.

Outage window to plan for: while digitalservice is still in `local` mode it
does not know the new key (its local hash is the old one), and after the flip
it does not know the old key (tenantcore holds a different hash). Either way
a re-issued tenant's storefront is refused from the moment its key is rotated
until its env is updated AND the flip is done. Keep that gap to minutes: have
the env change ready, and do the rotation, redeploy and step 5 in one sitting
for the mismatched tenants. Tenants that were not mismatched are unaffected.

Success per tenant: public read 200, admin sign-in works, and the tenant's
mismatch line no longer appears on a fresh step-2 dry run.

## Step 5. Flip

**REQUIRES THE OWNER'S EXPLICIT APPROVAL AT THE TIME (changes production
env). The assistant or automation must not perform it.**

On production digitalservice set `TENANT_RESOLVER=tenantcore` and restart.
Optional tuning, defaults shown: `TENANT_RESOLVE_RATE_PER_MINUTE=600`,
`TENANT_RESOLVE_BURST=120`. Startup fails closed: it refuses to boot if
`TENANTCORE_URL` or `TENANTCORE_SERVICE_KEY` is missing, or either limiter
number is below 1. A refusal to start is the safe outcome; fix the env, do
not revert blindly.

Checks:

```
curl -fsS "$DIGITALSERVICE_URL/readyz"
```

Success: 200; `features` contains `tenant_resolver_tenantcore` with
`enabled: true`; `degraded` is false; there is no `tenant_resolver` block (it
appears only while tenantcore is unreachable). Then, per tenant: one public
storefront request returns 200 with that tenant's key (set in your shell, not
pasted anywhere), and one admin sign-in succeeds. Watch error rates (401, 403,
503) for a stable period, for example 24 hours, before calling it done.
Expected failures to look for: 401 for a tenant whose key was re-issued but
whose env was not updated; 503 only if tenantcore is down and the key was
never cached.

## Step 6. Rollback

Set `TENANT_RESOLVER=local` (or unset it) on digitalservice and redeploy.

Works as-is for every tenant that was NOT re-issued. For a re-issued tenant
it does not (see the rollback caveat): its local hash is stale. Recover it
with a digitalservice-side key rotate via digitalservice's own rotate route
(owner approval, same care as step 4), then update that tenant's env files
and redeploy. Success: that tenant's public read returns 200 in `local` mode.

## Step 7. Known limits

- Fresh window 60 s: a resolved identity is reused without asking tenantcore.
- Stale grace 24 h: while tenantcore is unreachable, known keys keep being
  served from cache for up to 24 hours past the 60 s. A tenant suspended or
  re-keyed during an outage keeps working until the entry ages out or tenantcore
  returns. Past 24 h with nothing cached the answer is 503, never 401.
- Negative cache 30 s: a refused key is remembered; a freshly issued or
  re-issued key can be refused for up to 30 s after tenantcore accepts it.
- After a transport failure the client stops contacting tenantcore for 10 s
  and answers from cache; the cache holds at most 10000 entries.
- tenantcore on Render can cold start: the first call after idle can be slow
  (the client times out at 3 s and falls back to cache or 503). Warm it with
  the step 1 `curl` before the flip.
- Per-IP limiter in front of resolution only in `tenantcore` mode.
- Phase 3, removing digitalservice's duplicate management routes and the
  local lookup, is a separate plan.

## What changes for operators after the flip

- Key rotation, suspension, domain and status changes happen only in
  tenantcore (console Details page). They reach digitalservice within about 60
  seconds; a rotated key is refused after that.
- digitalservice still has its own tenant create / rotate / status / domain
  routes until phase 3. They no longer affect which key is accepted, and must
  not be used: they silently diverge from the truth.
- During a tenantcore outage the 24 h stale window applies (step 7); a
  suspension is not enforced on cached tenants until tenantcore is back.
- After issuing or re-issuing a key, allow up to 30 s for the negative cache.
- Avoid key changes for E&S near the end of its subscription (2026-10-20).

## Follow-ups

- A tenantcore to digitalservice hash back-copy tool, so rollback after a
  re-issue does not need a digitalservice-side rotate.
- Phase 3 plan: remove digitalservice's duplicate tenant management routes
  and the local lookup.
