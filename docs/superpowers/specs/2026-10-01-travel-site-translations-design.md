# Travel site translations (editable E&S wording) — design

Date: 2026-10-01. Status: draft for review.

## Goal

Staff of a travel tenant (first user: E&S Discovery Mongolia) change the wording of the public site — headlines, labels, FAQ, buttons — from the travel admin, in English, Mongolian and Korean, without a deploy.

Everything in `eandstravelmongolia/src/locales/{en,mn,ko}.json` is editable (354 leaf strings in 38 top-level groups; the three files have identical keys, 23 leaves are arrays). The admin menu entry is **Translations**, in the **System** section after Settings.

## Model: overrides on top of shipped wording

Copied from tenantcore's Site copy (`models/sitecontent.go`, `view/content.go`), moved to digitalservice and made per tenant, because tenantcore's copy belongs to the operator's own site and has no tenant.

- The site keeps its compiled-in JSON. The database holds **overrides** that the site merges on top.
- A key with no override uses the shipped wording. A key a developer adds in code works at once. If digitalservice is down, or the subscription has lapsed, the site shows the shipped wording.
- A stored value is a string, or an array (an array is one leaf, never indexed).
- A language with no stored value for a path falls back to the shipped value **for that language**, never to another language.

### Data

Collection `site_pages`, one document per `(tenant_id, page)`, unique index on that pair:

```
{ tenant_id, page, entries: [ { path, values: { en?, mn?, ko? } } ], updated_at, user_id }
```

`page` is a top-level key of the locale files (`hero`, `footer`, `tourDetail`, …). `path` is the dotted route inside that page: the string `hero.title` in the JSON is page `hero`, path `title`; `tourDetail.itinerary.day_label` is page `tourDetail`, path `itinerary.day_label`. Entries are a list, not a map, so that no BSON field name contains a dot.

### Validation (digitalservice)

digitalservice does not have the locale files, so it validates **shape**, not the existence of a key:

- `page`: 1–64 characters, `[A-Za-z0-9_-]`.
- `path`: 1–200 characters, `[A-Za-z0-9_.-]`, no empty segment. At most 1000 entries per page. No duplicate paths in one save.
- language keys: only `en`, `mn`, `ko`.
- a value is a string (at most 5000 characters), or an array of strings or of flat objects with string fields, at most 100 items. Nothing else (no numbers, no nested objects).
- the whole save body is at most 512 KB.

The site decides what a stored value means: it ignores an unknown path, and it ignores an override whose type differs from the shipped value (string over array, array over string). So a bad value can never blank or break a section.

## digitalservice API

Admin (bearer token, role `admin`, mounted in the existing gated `/admin` group). The subscription gate blocks only mutating methods, so a lapsed tenant can still read and open the editor, but cannot save. The list and get routes require the token too: unlike the older admin reads, they do not add another route that the public API key alone can call.

| Route | Purpose |
|---|---|
| `GET /api/v1/admin/translations` | list pages that have entries, with entry counts |
| `GET /api/v1/admin/translations/:page` | all entries for one page |
| `PUT /api/v1/admin/translations/:page` | replace the entries of one page |

`PUT` replaces the whole page, as tenantcore's does, so removing an override is saving without it. It records `updated_at` and the acting user. The page is created if it does not exist.

Public (X-API-Key only, in the storefront reads group; reads are never blocked by the gate):

`GET /api/v1/translations?lang=en` returns `{ "<page>": { "<path>": <value> } }` for one language, omitting pages with nothing for that language. `lang` must be `en`, `mn` or `ko`, otherwise 400. A language with no overrides returns `{}`.

All routes are added to `internal/api/docs/docs/openapi.json` (CRLF file), and the existing guard tests (`TestEveryTenantRouteRequiresAPIKey`, the OpenAPI route test) must stay green.

Edits take effect on the live site after the site's cache window (the existing 5-minute `revalidate` on GET reads).

## E&S site (`eandstravelmongolia`) — approach A

Today `getTranslation(locale)` is synchronous and called from 48 places; 19 files are client components that call it through `useTranslation`.

- `getTranslation(locale)` becomes **async** and returns the shipped JSON deep-merged with the overrides for that locale. Fetch uses the existing server client, so it reuses the 5-minute cache. On any error it returns the shipped JSON and logs once.
- The merge walks the shipped object; for each override path it replaces the leaf only if the leaf exists and the type matches. It never mutates the imported JSON.
- `[locale]/layout.tsx` calls it once and passes the result to a small client `TranslationProvider`. `useTranslation` reads from that context, so client components are unchanged apart from the hook.
- About 30 server call sites get an `await`. `generateMetadata` and pages call it per request, which is cheap because the fetch is cached.
- The provider ships the merged object to the browser; the JSON was already in the client bundle, so nothing new is exposed.

## Travel admin (`admin`)

- `src/lib/nav.ts`: add `{ href: '/translations', label: 'Translations' }` after Settings.
- `/translations`: list of pages with entry counts and a link each. `/translations/[page]`: editor.
- Editor: one row per path, English / Mongolian / Korean side by side. A string is a text area; an array is a list editor (add, remove, reorder; flat objects get one field per key). A language left blank means "use the shipped wording" and is not stored. A save sends the whole page.
- Empty state (no entries imported): explains that the site is showing built-in wording and how to import.
- Server actions and API client follow the existing pages (single quotes, no semicolons; the API client stays server-only).
- Layout and component style copied from `innonomads/admin` `ContentEditor` and the content pages, adapted to this app's tokens.

## Seeding

`eandstravelmongolia/scripts/export-translations.mjs` flattens the three JSON files into `{ page, entries }` per top-level key and, with `--push`, calls `PUT /admin/translations/:page` for each page that has no entries yet (using a tenant admin login from the environment). It is a one-off per environment and never overwrites a page that already has entries. Without `--push` it prints the JSON.

The editor therefore opens on the real current wording, and an unedited key is stored equal to the shipped value, which is harmless because the merge is an override.

## Out of scope

- Editing the locale structure (adding or removing keys) — that stays a code change.
- Languages beyond en, mn, ko.
- Version history, drafts or a publish step: a save is live after the cache window.
- A preview pane (tenantcore's has one; the E&S site has no preview route).
- Cache purge on save.

## Testing

- digitalservice: unit tests for the validation rules (each limit, bad language, duplicate path, wrong value type), the replace semantics, tenant isolation (tenant A cannot read or write tenant B's page), the per-language public view, and that a lapsed subscription answers `PUT` with the same 402 as other gated routes while `GET` still works. The service uses narrow interfaces and fakes, like the password reset service. Mutation-check the tenant-isolation and type rules.
- Site: unit tests for the merge (replaces a matching leaf; ignores an unknown path; ignores a type mismatch; does not mutate the shipped object; empty overrides return the shipped object; a failed fetch returns the shipped object). `tsc` and `eslint` clean.
- Admin: `tsc`, `eslint`, then a click-through in the browser (list, edit a string, edit an array, save, reload, blank a language).
- Live: seed a throwaway change on one string, confirm it shows on the site after the cache window, revert it.

## Risks

- `getTranslation` becoming async touches many files. A missed `await` yields a Promise where an object is expected, which `tsc` catches.
- A stored array override could have different item shape from the shipped one. The site's merge therefore checks arrays by item kind: if the shipped array's first item is a string, every override item must be a string; if it is an object, every override item must be an object whose keys include all of the shipped item's keys with string values. Otherwise the shipped array is kept.
- An override equal to the shipped value is stored after seeding; later code edits to the JSON for that key will not show until the override is removed. Accepted for the first version.
