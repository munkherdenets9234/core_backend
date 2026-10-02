# Translations Base Snapshot Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A stored translation that a person has not changed stops being an override, so later edits to the site's shipped wording reach the site, and new shipped keys can be added to already-imported pages.

**Architecture:** Each stored value gets a `base` (a copy of the shipped wording it was seeded from). The public read omits a value equal to its `base`. The admin editor passes `base` through untouched. The importer sets `base` on new pages and gains a `--sync` mode that attaches `base` to existing data, adds new paths and refreshes unedited values.

**Tech Stack:** Go + Gin + MongoDB (digitalservice); Node 20 ESM with the built-in test runner (site importer, admin helpers); Next.js 16 / React 19 (admin).

**Spec:** `docs/superpowers/specs/2026-10-01-travel-site-translations-design.md`, section "Addendum (2026-10-02)". It builds on `docs/superpowers/plans/2026-10-01-travel-site-translations.md`, already shipped. Repos: `digitalservice` (branch `refactor/backend-core`), `admin` (master), `eandstravelmongolia` (master).

## Global Constraints

- `base` has the same shape and limits as `values`: languages `en`, `mn`, `ko` only; a value is a string (at most 5000 chars) or an array (at most 100 items) of strings or of flat objects whose fields are all strings (keys `[A-Za-z0-9_-]{1,64}`, at most 20 per object). It is optional.
- The server never derives `base`; it stores what it is sent. The 512 KB body cap and 1000-entry cap are unchanged and already count `base`, because the cap is on the request body.
- Public read `GET /api/v1/translations?lang=` returns `values[lang]` for a path only if `base[lang]` is absent or `values[lang]` is not deeply equal to `base[lang]`. Equal means same type, same string, same array items in the same order, same object fields (object key order does not matter). Admin reads return everything including `base`, unfiltered.
- Blank cleaning: a language with a blank value is dropped from `values` but its `base` is kept; an entry with no non-blank value in any language is dropped together with its `base`.
- Importer default mode: the never-overwrite rule is untouched (a page that has entries is never written); a newly imported page stores `base` equal to the shipped value for every entry and language it sends.
- `--sync` rules, per page, per shipped entry (after the same storable-entry filter), per language the shipped file has: path not stored: add it with `values = base = shipped`; stored with no `base`: set `base = shipped`, keep `values`; stored with `base` and `values == base` (unedited): set `values = base = shipped`; stored with `base` and `values != base` (edited): keep `values`, set `base = shipped`. Stored entries no longer shipped are left alone. No change means no write. Any GET that is not 2xx + JSON + `success === true` + array `data.entries` is `failed` with no write.
- digitalservice: `go build ./... && go vet ./internal/... ./pkg/... && go test ./internal/... ./pkg/... -count=1`; never `go test ./...`. `internal/api/docs/docs/openapi.json` is CRLF: edit as bytes.
- Site and admin: single quotes, no semicolons; no new dependencies; never edit `package.json` (the pre-commit hook runs `npm audit` and blocks on pre-existing vulnerabilities); never `--no-verify`. Site tests: `node --test src/lib/translations/`. Admin tests: `node --test src/lib/translations-edit.test.mjs`.
- Never print `.env` contents, tokens or passwords. Live runs need the user's sign-in; ask, do not guess credentials.

## Review Focus

1. A value and a `base` that look alike but differ in type (`"1"` vs `["1"]`, string vs one-item array, object vs string) are NOT equal and the value stays an override. Pinned in Tasks 1 and 2.
2. Arrays compare in order (`["a","b"]` is not `["b","a"]`); objects compare by fields, not key order. Pinned in Tasks 1 and 2.
3. A value edited and then set back to equal its `base` drops out of the public read with no other action. Pinned in Task 1.
4. Existing data with no `base` behaves exactly as before until synced. Pinned in Task 1.
5. `--sync` must never write when the GET was unreadable, never lose a person's edit, and never write when nothing changed. Pinned in Task 2.

---

## Task 1: `base` on the server (digitalservice)

**Files:**
- Modify: `internal/models/site_page.go`, `internal/service/site_page_service.go`, `internal/repository/site_page_repo.go`, `internal/api/docs/docs/openapi.json`
- Test: `internal/service/site_page_service_test.go`, `internal/repository/site_page_repo_test.go`

**Interfaces:**
- Produces: `ContentEntry` gains `Base map[string]any \`bson:"base,omitempty" json:"base,omitempty"\``; `func siteValuesEqual(a, b any) bool` in the service package (strings equal; `[]any` equal element-wise in order; `map[string]any` equal key-wise; any type difference is false); `validateEntries` validates and cleans `Base` (unknown language is a BadRequest `entry N: unknown language`; each base value passes `checkSiteValue`, errors read `entry N (lang) base: <rule>` and never echo submitted text; blank base languages are dropped; `Base` is nil when empty); `Public` omits a value when `Base[lang]` exists and `siteValuesEqual(Values[lang], Base[lang])`; the repository's `normalizePage` also runs `normalizeBSON` over every `Base` value.

- [ ] **Step 1: Write the failing tests** (service, with the existing fake store): `TestSiteValuesEqualTreatsDifferentTypesAsDifferent` (`"1"` vs `[]any{"1"}`, string vs map, `[]any{"a"}` vs `[]any{map[string]any{"a":"x"}}`), `TestSiteValuesEqualComparesArraysInOrder`, `TestSiteValuesEqualIgnoresObjectKeyOrder`, `TestPublicOmitsAValueEqualToItsBase` (string, array, object array), `TestPublicKeepsAValueThatDiffersFromItsBase`, `TestPublicKeepsAValueWithNoBase` (Review Focus 4), `TestPublicDecidesPerLanguage` (en equal to base omitted, mn edited kept, same entry), `TestSaveStoresBase`, `TestSaveRejectsAnUnknownBaseLanguage`, `TestSaveRejectsABadBaseValue` (number, nested object, 5001 chars, 101 items; message contains no submitted text), `TestSaveKeepsBaseForALanguageWhoseValueIsBlank`, `TestSaveDropsBaseWithAnEntryThatIsBlankInEveryLanguage`, `TestGetReturnsBaseUnfiltered`, `TestEditedThenResetToBaseDropsOutOfThePublicRead` (Review Focus 3: save value=edited, then save value=base, `Public` omits it). Repository: `TestNormalizePageNormalizesBase` (a `primitive.A` holding `primitive.D` inside `Base` marshals to plain JSON).
- [ ] **Step 2:** Run `go test ./internal/service ./internal/repository -count=1` — expect FAIL (undefined).
- [ ] **Step 3:** Implement the model field, `siteValuesEqual`, base validation in `validateEntries`, the `Public` filter, and `normalizePage` for `Base`. Add `base` to the entry schema in `openapi.json` with its description, preserving CRLF and adding lines only.
- [ ] **Step 4:** Run the Task check command — expect green. Mutation-check: make `Public` ignore `Base`, make equality ignore type, and make `Save` drop `Base`; each must fail a named test above, then restore.
- [ ] **Step 5:** Commit in digitalservice: `feat: store a base snapshot and skip unchanged values in the public read`.

## Task 2: Admin passes `base` through (admin)

**Files:**
- Modify: `src/lib/types.ts`, `src/lib/translations-edit.mjs`, `src/lib/translations-edit.d.mts`, `src/components/admin/TranslationsEditor.tsx`
- Test: `src/lib/translations-edit.test.mjs`

**Interfaces:**
- Consumes: Task 1 admin reads now include `base`.
- Produces: `TranslationEntry` gains `base?: Partial<Record<Locale, TranslationValue>>`; editor rows `{ path, kind, keys, values, base? }` carry `base` from load; `serialize(rows, locales)` returns `{ path, values, base? }[]` where `base` is the row's `base` copied unchanged and is present only when the row has a non-empty `base` and the entry has at least one non-blank value (an entry omitted for being blank takes its `base` with it). The editor never edits `base`.

- [ ] **Step 1: Write the failing tests:** `serialize keeps base unchanged`, `serialize omits base when the row has none`, `serialize drops base with an entry that is blank in every language`, `serialize keeps base for a language whose value was blanked` (value blank, base present: base still sent), `base survives load to serialize for string, strings and objects kinds` (round trip with deep equality, key order preserved).
- [ ] **Step 2:** Run `node --test src/lib/translations-edit.test.mjs` — expect FAIL.
- [ ] **Step 3:** Implement in `translations-edit.mjs` and the `.d.mts`; carry `base` through `toRows` and the save payload in `TranslationsEditor.tsx`; add the type. Do not render `base`.
- [ ] **Step 4:** Run the tests (green); `npx tsc --noEmit` exit 0; `npx eslint` on touched files exit 0 (`npx eslint src` has a pre-existing error in `(dashboard)/tours/page.tsx`).
- [ ] **Step 5:** Commit in admin: `feat: keep the base snapshot when saving translations`.

## Task 3: Importer sets `base` and gains `--sync` (eandstravelmongolia)

**Files:**
- Create: `src/lib/translations/sync.mjs`, `src/lib/translations/sync.d.mts`, `src/lib/translations/sync.test.mjs`
- Modify: `scripts/export-translations.mjs`, `src/lib/translations/push.mjs` (only if needed to share the strict-GET helper), `src/lib/translations/push.test.mjs` (extend)

**Interfaces:**
- Consumes: `validateValue`, `filterEntries`, `tooLarge`, `MAX_ENTRIES`, `MAX_BODY_BYTES`, `pushPages` from `push.mjs`; `flattenTranslation` from `merge.mjs`.
- Produces in `sync.mjs`: `valuesEqual(a, b): boolean` (same semantics as the server's `siteValuesEqual`); `planSync(stored, shipped): { entries, changed }` where `stored` and `shipped` are `{ path, values, base? }[]` and `entries` is the merged list (stored order first, then newly added paths in shipped order; stored entries not shipped are copied verbatim); `syncPages({ pages, fetchImpl, baseUrl, headers, dryRun = false, log = console.log })` returning `{ created, synced, unchanged, skipped, tooLarge, failed, wouldSync }`: per page `GET` (strict, as in `pushPages`); stored empty: `PUT` the shipped entries with `base` set (counts `created`); otherwise `planSync`; `!changed`: `unchanged`; dry-run: log `would sync <page> (<n> changes)` and no `PUT`; else size guard on the merged list, `PUT`, then re-`GET` and require the same entry count else `failed`.
- Modified: in the script, entries built for the default import carry `base` equal to a deep copy of `values`; a new flag `--sync` is accepted only together with `--push` (otherwise exit non-zero with `--sync needs --push`); `--sync` runs `syncPages`; `--sync` with `--dry-run` reads but never writes. The default mode and its never-overwrite tests stay unchanged and green.

- [ ] **Step 1: Write the failing tests** (`sync.test.mjs`, fake fetch recording method and body): `valuesEqual` cases from Review Focus 1 and 2 (type differences, array order, object key order); `planSync adds a path that is not stored with values and base equal to shipped`; `planSync attaches base to a stored entry that has none and keeps its values`; `planSync refreshes an unedited value to the new shipped text and its base`; `planSync keeps an edited value and refreshes only its base`; `planSync decides per language`; `planSync leaves stored entries that are no longer shipped`; `planSync reports changed false when nothing differs`; `syncPages never writes when the GET is a 500, non-JSON, success:false, or has non-array entries` (Review Focus 5, calls equal `['GET']`); `syncPages makes no PUT when nothing changed`; `syncPages dry-run makes no PUT`; `syncPages creates a page that has no stored entries with base set`; `syncPages fails the page when the re-GET entry count differs`; `syncPages never loses an edit` (stored edited value is present in the PUT body). In `push.test.mjs`: `default import sends base equal to values`.
- [ ] **Step 2:** Run `node --test src/lib/translations/` — expect FAIL.
- [ ] **Step 3:** Implement `sync.mjs`, its `.d.mts`, and the script changes (flag parsing, `base` on new entries, summary lines, size guard on the merged list). Keep every request error message free of headers and secrets.
- [ ] **Step 4:** Run `node --test src/lib/translations/` (all green) and `node scripts/export-translations.mjs` with no flags (still 37 pages; print mode now includes `base`); `npx tsc --noEmit` exit 0. Do NOT run `--push`.
- [ ] **Step 5:** Commit in eandstravelmongolia: `feat: import with a base snapshot and add a sync mode`.

## Task 4: Live migration and verification

- [ ] **Step 1:** Restart digitalservice with the Task 1 build (`PORT=8080`, tenantcore running). Sign in as the user (ask them for the password; never store it) to get an admin token for the run.
- [ ] **Step 2:** `node scripts/export-translations.mjs --sync --push --dry-run` against E&S: expect every page to report changes (bases to attach) and none failed. Then run it without `--dry-run`. Run it a second time: expect every page `unchanged`.
- [ ] **Step 3:** Confirm the public read `GET /translations?lang=mn` returns `{}` (nothing is edited). Edit one string through the admin UI; confirm the public read now returns only that value; set it back in the UI; confirm it returns `{}` again and the live site shows the shipped wording.
- [ ] **Step 4:** Shipped-wording change: copy the E&S locale files to a throwaway directory, change one string and add one new key there, point a one-off run of the script at it (or temporarily edit and restore, with `git status` clean afterwards), run `--sync --push`, and confirm an unedited entry follows the new text, the new key is stored, and an entry edited in the UI keeps its edit. Restore everything and run `--sync --push` once more so E&S matches the real files.
- [ ] **Step 5:** Update `tenantcore/HANDOVER-translations-2026-10-01.md` (the "Imported strings are frozen copies" item becomes resolved and describes `--sync`) and the memory note `travel-site-translations.md`; commit the handover in tenantcore.
