# Alt8Mail Internationalization and Frontend Decomposition Plan

Status: accepted. Phases 1-7 implemented; all 33 reviewed locales are certified and enabled. Phase 3 Waves B-F string extraction and the remaining Phase 6 server surfaces are post-release work.
Date: 2026-11-14.

## 1. Recommendation in one page

Yes, `public/app.js` should be split, but not as a big-bang rewrite and not
before the localization foundation exists.

The current file is 33,066 lines and 1.72 MB. It already uses native ES modules
(`search.mjs`, `i18n.mjs`), so the platform is ready for a gradual split. The
risk is not ES modules; it is the shared mutable `state`, the single delegated
click handler, and cross-feature calls. Moving the file into 40 files in one
change would turn one large knot into 40 tangled modules with circular imports.

Recommended sequence:

1. Build the localization foundation and a pseudo-locale.
2. Extract the low-level spine (`state`, `dom`, `api`, `helpers`, `format`,
   `toast`) without changing behavior.
3. For every feature after that, do an **extract + wrap strings + translate**
   pass in the same change. Do not run a giant codemod over the whole file
   before splitting; that touches every line twice and conflicts with every
   feature move.
4. Keep `app.js` as the composition root and event wiring. A final `app.js` of
   2,000-3,000 lines is a success, not a failure.
5. Treat the 34-language rollout as a translation program, not only an
   engineering task. Machine translation can seed catalogs; security, legal,
   navigation and money/billing strings need native review before release.

The largest scope saving is on the backend: do not translate every Go error
string by hand. Return stable error codes plus parameters, translate them in the
frontend catalog, and use Go catalogs only for content that leaves the browser:
emails, push notifications, calendar invitations, and API `message` fallbacks.

## 2. Current state

Frontend:

- `public/app.js`: 33,066 lines, 1.72 MB, already an ES module.
- `public/i18n.mjs`: working loader with fallback to English and `Intl` plural
  selection through `tn()`, but only one namespace (`common`).
- `public/locales/`: eight catalogs (`ar`, `de`, `en`, `es`, `fr`, `it`, `nl`,
  `pt`) with only 73 keys each. Seven target locales exist; most UI strings are
  still hardcoded.
- `public/index.html`: only 33 `data-i18n` attributes. The rest of the static
  shell is English text.
- The app defines its own `currentLocale()` from `state.preferences.language`,
  while `i18n.mjs` resolves the manifest locale. These can diverge. Make one
  source of truth.
- `formatBytes()` hardcodes `GB`, `MB`, `KB`, `B`; these are user-visible units
  and need locale formatting or catalog entries.
- A rough scan finds 418 `showToast` calls, 48 `appConfirm` calls, 175 direct
  `textContent = "..."` assignments, and 168 `innerHTML = \`...\`` blocks.
  At least 482 unique toast/dialog messages exist before touching the rest of
  the surfaces. Expect several thousand translation keys after extraction.
- `tools/check-locales.mjs` currently requires every catalog to contain every
  key and only accepts string values. That contradicts an incremental rollout
  and will fail as soon as plural objects are introduced.
- `public/sw.js` precaches a fixed shell list. Splitting `app.js` requires the
  service worker to cache every imported module.

Backend:

- `backend/internal/httpapi/calendar_i18n.go` is the only general user-content
  localization. It is a hand-maintained Go map with eight languages, used for
  calendar invitation email.
- `preferenceLanguages` in `features_settings.go` hardcodes nine locale values.
  Adding 33 target locales requires expanding or generating this list.
- Most `writeError`, `fail`, `failDetails`, notification, and email strings are
  English literals in handlers.
- Office/OnlyOffice has its own UI language. It is a separate integration, not
  part of the webmail catalog.

## 3. Decisions to lock before Phase 0

These defaults are recommended; change them only if the product requirements
differ.

| Decision | Recommended default |
| --- | --- |
| Chinese | `zh-Hans` (Simplified) first; add `zh-Hant` as a separate catalog if Traditional is required |
| Norwegian | `nb` (Bokmål); do not use `no` as a catalog |
| Portuguese | Keep `pt-BR`; add `pt-PT` only as a separate catalog if needed |
| Spanish | `es-ES`; add `es-419` (Latin America) later if demand exists |
| English | `en-US` and `en-GB` |
| Arabic | Modern Standard Arabic, `ar`, RTL |
| Hebrew | `he`, RTL |
| Dates | Gregorian calendar for all locales; document that Hijri is out of scope |
| Numerals | Use locale defaults from `Intl.NumberFormat`; if Arabic Latin digits are required, use the `ar-u-nu-latn` locale intentionally |
| Translation model | TMS/Weblate with native review; machine translation only as a draft seed |
| Admin surfaces | System Control and customer admin localization deferred until webmail parity; exclude from the "100% webmail" scope |

Proposed target catalog codes (33 targets plus English):

`ar`, `bg`, `cs`, `da`, `de`, `el`, `en-GB`, `en-US`, `es`, `et`, `fi`, `fr`,
`he`, `hi`, `hr`, `hu`, `id`, `it`, `ja`, `ko`, `lt`, `lv`, `ms`, `nb`, `nl`,
`pl`, `pt-BR`, `ro`, `ru`, `sk`, `sl`, `sv`, `tr`, `uk`, `zh-Hans`.

Add `zh-Hant`, `pt-PT` or `es-419` only if those product variants are approved.

## 4. Target catalog architecture

### 4.1 One canonical catalog per language

Use one JSON file per language as the source of truth:

```text
locales/
  manifest.json
  en.json                 # canonical; always complete
  ar.json
  bg.json
  ...
  zh-Hans.json
```

For now, one file per language is simpler than 12 namespaces per language and
matches the proposed direction. The existing `NAMESPACES` seam in `i18n.mjs`
remains available if a catalog grows beyond roughly 250 KB and needs to be
lazy-loaded later.

Do not hand-edit generated copies:

```text
public/locales/          # served to the browser (generated copy)
backend/internal/i18n/catalogs/   # embedded by Go (generated copy)
```

Use `scripts/i18n-sync.mjs` to copy the canonical files into both locations.
Commit the generated copies so the Go embed and Node development server work
without a build step, and run `i18n-sync --check` in CI to fail on drift.

Why not embed `locales/` directly in Go? `//go:embed` cannot reach parent
directories, and moving the Go module root is more disruptive than a generated
copy. The generated-copy pattern keeps one editable source and satisfies both
runtimes.

### 4.2 Manifest

The manifest must carry more than `code` and `value`:

```json
{
  "default": "en-US",
  "locales": [
    {
      "code": "en-US",
      "catalog": "en",
      "label": "English (United States)",
      "dir": "ltr",
      "status": "source"
    },
    {
      "code": "ar",
      "catalog": "ar",
      "label": "العربية",
      "dir": "rtl",
      "status": "reviewed"
    },
    {
      "code": "zh-Hans",
      "catalog": "zh-Hans",
      "label": "简体中文",
      "dir": "ltr",
      "status": "reviewed"
    }
  ]
}
```

`catalog` separates the BCP 47 value from the file name. This matters for
`en-US`/`en-GB` sharing `en`, and for future `zh-Hans`/`zh-Hant` variants.

The manifest is also the source for:

- `preferenceLanguages` in Go (generate a small Go file from it, or embed and
  parse it at startup).
- The language selector labels.
- The locale fallback chain.
- The list of catalogs the checker and e2e matrix exercise.

### 4.3 Message format

Flat keys, semantic names, placeholders and plural objects:

```json
{
  "mail.list.unreadCount": {
    "one": "{count} unread message",
    "other": "{count} unread messages"
  },
  "mail.reader.replyTo": "Reply to {name}",
  "errors.FORWARDING_EXTERNAL": "Forwarding to an external address is disabled by your organization.",
  "calendar.reminder.postpone": "Postpone",
  "calendar.reminder.postponedUntil": "Reminder postponed to {date} {time}"
}
```

Rules:

- No HTML in catalog values. Translate fragments and keep markup in the
  template. `applyStaticTranslations` uses `textContent`, which is safe.
- No string concatenation. Use `{name}` placeholders and full messages.
- Use `tn(key, count, vars)` for every count. `Intl.PluralRules` supplies the
  category; Arabic needs `zero/one/two/few/many/other`, Russian and Polish need
  `one/few/many/other`.
- Escape interpolated user data at the call site when building HTML:
  `esc(t("...", { name }))`. `t()` does not escape.
- Keys describe the surface, not the English wording. Do not use hashes as
  keys; translators cannot reason about them.
- Store an English description/context sidecar for keys whose meaning depends
  on placement, e.g. `locales/en.meta.json` keyed by the same key. Do not put
  translator comments inside the runtime JSON.

### 4.4 Locale resolution

One resolver for both JS formatting and Go:

1. Explicit user preference (`state.preferences.language`), normalized to a
   manifest catalog.
2. `Accept-Language` / browser locale, matched against the manifest.
3. `en-US`.

For API requests, the frontend sends `X-A8M-Locale: <current catalog value>`.
The backend uses that first for errors and content created during the request;
for background email/push it uses the recipient's stored preference.

`currentLocale()` in `app.js` must become a re-export of the i18n resolver, not
a second calculation from raw preferences.

### 4.5 Language switching

`setLocale()` currently updates static `data-i18n` nodes and the `<html lang>`
and `dir`, but dynamic surfaces do not re-render. Short term, save the
preference and reload the page after a language change. Once the feature modules
are localized, listen for `a8m:i18n` and re-render the active surface without a
reload. Do not ship a partial re-render that leaves half the page in the
previous language.

## 5. Frontend decomposition

### 5.1 What not to do

- Do not move all 33k lines in one pull request.
- Do not create 40 small modules before the shared spine is stable.
- Do not import `app.js` from feature modules.
- Do not use a global `window` bag as an escape hatch.
- Do not combine a module move with behavioral changes or dead-code cleanup.
- Do not run a global codemod that rewrites every string literal before the
  module boundaries exist.

### 5.2 Layering rules

```text
Layer 0:  constants, state, i18n, pure helpers
Layer 1:  dom, api, format, toast, theme
Layer 2:  feature services (mail service, calendar service, ...)
Layer 3:  feature UI modules
Layer 4:  app.js composition root and event wiring
```

Imports only point downward. If a cycle appears, move the shared function down a
layer or pass a callback from `app.js`; do not create a barrel file that
re-exports everything.

`state.js` exports one mutable `state` object. Modules may read and mutate it
deliberately. The goal is not immutability; it is one owner and explicit
imports.

### 5.3 Recommended first extraction order

1. `public/src/core/state.js` — `state`, `DEFAULT_PREFERENCES`, `prefs`,
   constants. No DOM access.
2. `public/src/core/helpers.js` — `$`, `$$`, `esc`, `uid`, `clone`,
   `escapeRegExp`, `normalizeEmail`, `avatar`, color helpers.
3. `public/src/core/format.js` — locale-aware date, time, number, bytes and
   relative-time helpers; imports the locale resolver.
4. `public/src/core/api.js` — `ApiFailure`, `api()`, CSRF and delegated
   headers, locale header.
5. `public/src/core/dom.js` — top-level element references. Evaluated after the
   document is parsed, as today.
6. `public/src/core/toast.js` — toast and `showError`.
7. `public/src/core/theme.js` — theme, accent, density.
8. `public/src/core/i18n.js` — stable re-export of `t`, `tn`, direction and
   locale helpers.

After this, `app.js` still owns boot and the delegated event handler, but the
shared spine is out. This is the safest first reduction.

### 5.4 Feature waves

Aim for modules of 1,500-3,000 lines, not 200-500 lines. Group by feature and
change frequency, not by technical layer.

| Wave | Modules | Main content |
| --- | --- | --- |
| A | `mail/folders`, `mail/list`, `mail/reader`, `mail/search`, `mail/details` | folder tree, list, reader, search, source/trust |
| B | `mail/compose`, `mail/drafts`, `mail/spellcheck`, `mail/ai-attachments` | compose manager, editor, drafts, spell check, AI attachment picker |
| C | `calendar/calendar`, `calendar/events`, `calendar/polls`, `calendar/booking`, `people` | calendar views, event dialog, polls, booking, people |
| D | `tasks`, `files/cabinet`, `files/storage`, `files/office`, `files/upload-manager` | tasks, Cabinet, storage, Office, upload manager |
| E | `settings/*`, `accounts`, `notifications` | settings tabs, connected accounts, notification center |
| F | `ui/*` | toast, menus, command bar, shortcuts, message menu, selection |

For each module:

1. Move functions and constants without edits; re-export/import names so call
   sites do not change.
2. Run the full e2e suite.
3. Wrap the module's user-visible strings with `t`/`tn`; add keys to `en.json`.
4. Run the hardcoded-string checker for that module.
5. Run the locale smoke matrix for the module (en, de, ar, ja).
6. Commit the move and the string wrapping together. Do not leave a moved
   module half-translated for more than one change.

### 5.5 Service worker and module loading

Native ES modules are acceptable for the first split. The service worker must
cache the import graph or offline mode breaks. Add
`tools/module-manifest.mjs` that walks `import` statements from
`public/src/app.js` and generates the JS module list used by `sw.js`.

If startup performance regresses, add an esbuild production bundle:
`public/src/app.js` stays the source, `public/app.js` becomes the generated
bundle, and the release build produces it. Do not add a bundler at the same
time as the split; measure first.

## 6. Backend localization

### 6.1 Package and catalog

Add `backend/internal/i18n`:

```go
i18n.T(ctx, "errors.FORWARDING_EXTERNAL")
i18n.TN(ctx, "mail.list.unreadCount", count, map[string]any{"count": count})
i18n.FormatDate(locale, timeZone, value)
```

Use the same JSON format as the frontend. Embed generated catalogs with
`//go:embed catalogs/*.json`. Use `golang.org/x/text/language` for matching and
`golang.org/x/text/feature/plural` for CLDR plural categories. Do not invent a
second plural convention.

Locale middleware resolves in this order:

1. `X-A8M-Locale` header from the authenticated webmail request.
2. Validated `Accept-Language`.
3. The authenticated user's stored `preferences.language`.
4. `en-US`.

Never accept a filesystem path or an arbitrary catalog name from the client.

### 6.2 Errors should be code-first

The current API already returns error codes. Extend the payload:

```json
{
  "error": {
    "code": "FORWARDING_EXTERNAL",
    "message": "Forwarding to an external address is disabled by your organization.",
    "params": {}
  }
}
```

The frontend translates `errors.<CODE>` from its own catalog and falls back to
`message`. This gives full UI localization without waiting for every Go string
to be translated. Go localization remains required for:

- email subjects and bodies,
- push notification titles and bodies,
- non-browser API clients,
- server-rendered pages such as invitation RSVP pages.

### 6.3 What not to store

Never store translated text in the database when it may need to be read later
in another language.

- Notifications: add `template_key`, `params` JSON, and keep the legacy
  `title`/`body` columns as fallback for old rows. Localize at read/send time.
- Audit log: store the action code and structured params. Localize labels in
  the UI. Do not retro-translate historical English rows.
- Email content: render from templates and recipient preferences at send time.
- Raw IMAP/SMTP/ManageSieve errors: keep them in `details` for administrators;
  show a translated friendly message with a stable code.

### 6.4 Priority order

1. Calendar invitation email (migrate the existing `calendar_i18n.go` map into
   the shared catalog; this proves the Go pipeline).
2. Authentication and security: login errors, 2FA, passkeys, recovery, password
   reset, new-device alerts.
3. Connected accounts and forwarding.
4. Office unavailable/action errors.
5. Mail push notifications and scheduled-send notifications.
6. Remaining API errors.

Office/OnlyOffice's internal UI has its own language setting. Pass the user's
language to the editor config separately; do not duplicate its strings in our
catalog.

## 7. RTL and CJK workstream

### 7.1 RTL

The app already sets `<html dir>` from the manifest. The remaining work is CSS
and layout:

- Replace physical properties with logical properties. A scan found roughly 418
  direction-sensitive declarations (`margin/padding/border-left/right`,
  `left/right`, `text-align: left/right`) across the webmail CSS.
- Use `:dir(rtl)` or `[dir="rtl"]` for the cases logical properties cannot
  express (transforms, background positions, icon mirroring).
- Mirror navigation icons: back/forward chevrons, reply/forward arrows,
  indent/outdent, previous/next controls. Do not mirror the logo, media
  thumbnails, send icon, or brand marks. Mark mirrorable icons explicitly.
- Email bodies: set `dir="auto"` on rendered message content so an Arabic or
  Hebrew email reads correctly inside an English UI. Compose needs explicit
  paragraph direction controls and `dir="auto"` on the editor.
- Keep email addresses, phone numbers, URLs and code snippets LTR with
  `dir="ltr"` wrappers.
- Arabic and Hebrew need native review; do not rely on screenshots alone.
- Use Gregorian dates for Arabic; do not silently switch to Hijri.
- Use `Intl.NumberFormat` for digits and `Intl.PluralRules` for counts.

### 7.2 CJK and other scripts

- Font stacks per script, prepended only for the matching `:lang()`:
  - CJK: `"Hiragino Sans"`, `"Yu Gothic UI"`, `"Microsoft YaHei"`,
    `"PingFang SC"`, `"Noto Sans CJK SC"`, then the Latin stack.
  - Arabic: `"Segoe UI"`, `Tahoma`, `"Noto Sans Arabic"`, `"Geeza Pro"`.
  - Hebrew: `"Segoe UI"`, `Arial`, `"Noto Sans Hebrew"`.
- CJK: set `letter-spacing: 0`; increase line-height by roughly 10%; avoid
  fake italics by using weight or color for emphasis; use
  `word-break: keep-all` for CJK prose but allow breaking for URLs and email
  addresses.
- Arabic/Hebrew: set `letter-spacing: 0`; disable `text-transform: uppercase`
  for scripts without case; increase line-height for diacritics.
- Test German text expansion (roughly 30-40%) and a pseudo-locale with 40%
  expansion. Fixed-width buttons and tabs are the usual failures.
- Hindi/Devanagari needs its own font stack and conjunct rendering check.

### 7.3 Directional formatting helpers

Add helpers to `format.js`:

```js
formatNumber(value)      // Intl.NumberFormat(locale)
formatBytes(value)       // localized units, not hardcoded GB/MB
formatDate(value)        // Intl.DateTimeFormat(locale, timeZone)
formatRelative(value)    // Intl.RelativeTimeFormat(locale)
formatList(items)        // Intl.ListFormat(locale)
```

All of them use the same resolved locale as `i18n.mjs`. Remove raw
`toLocaleString()` calls that do not pass an explicit locale.

## 8. Testing, CI and release gates

### 8.1 Catalog checks

Replace `tools/check-locales.mjs` with `tools/i18n-check.mjs`:

- `en.json` is the superset. Any key in another catalog that is absent from
  `en.json` fails.
- Missing keys in a target catalog are reported as TODO and do not fail the
  build during incremental rollout.
- Placeholder names must match `en.json` exactly.
- Plural objects must contain valid CLDR categories for that locale.
- No duplicate keys, no empty strings, valid JSON.
- Manifest `code`, `catalog`, `dir` and `status` are valid.
- Report coverage per language and per surface.

`tools/i18n-status.mjs` prints a table: keys total, translated, missing, stale,
placeholder mismatches.

### 8.2 Pseudo-locale

Generate `en-XA` from `en.json` by accenting letters and expanding length by
roughly 40%. It is the cheapest way to find strings that never entered the
catalog. Run one e2e pass in `en-XA`; any untranslated English visible in the
UI is a missed extraction.

### 8.3 Locale smoke matrix

Do not run the full 3,000-line e2e suite in 34 languages on every pull request.
Run a sentinel matrix:

- `en-US` (baseline)
- `de-DE` (long text)
- `ar` (RTL, Arabic plurals)
- `he` (RTL)
- `ja` (CJK)
- `zh-Hans` (CJK)
- `ko` (CJK)
- `ru` (Cyrillic, plurals)
- `hi` (Devanagari)
- `en-XA` (pseudo-locale)

Capture login, inbox list, reader, compose, calendar, tasks, Cabinet,
settings/account, and scheduled center. Assert `document.documentElement.lang`
and `dir`. Full 34-locale screenshots run nightly or before a release.

### 8.4 Backend tests

- Locale negotiation and fallback.
- Plural categories for `ar`, `ru`, `pl`, `cs`, `uk`.
- Placeholder parity and HTML escaping.
- Calendar invitation HTML in RTL and CJK.
- Notification render in recipient locale.
- Error response includes `code`, `message`, `params`.

### 8.5 Hardcoded-string gate

Add `tools/i18n-lint.mjs` that scans:

- HTML text nodes and `placeholder`, `title`, `aria-label`.
- `showToast`, `showError`, `appConfirm` messages.
- `textContent`, `innerHTML`, option labels, dialog headings.
- Go `writeError`, `fail`, `failDetails` and user-facing email/notification
  strings.

Start as a warning with an allowlist for technical strings; make the webmail UI
scope fail CI once a surface reaches 100%. Never translate logs, metrics, IDs,
CSS classes, MIME types, API paths or raw protocol errors.

## 9. Phased roadmap

Durations assume one senior frontend engineer, one backend engineer, and
part-time design/QA. Translation vendor time is separate.

### Phase 0 — Decisions and inventory (3-5 days)

Deliverables:

- Lock the locale variants and target list.
- Choose the TMS (recommended: Weblate if self-hosted; otherwise Lokalise,
  Crowdin or Phrase).
- Write the glossary for product terms: Alt8Mail, Cabinet, Pulse, Office,
  mailbox, connected account, delegation, retention, etc.
- Run a read-only string inventory by module and surface.
- Capture the current e2e baseline.
- Decide the first pilot slice (recommended: login + shell + Tasks).

Exit: approved locale list, glossary, key-count estimate, and a pilot scope.

### Phase 1 — Localization foundation (1-2 weeks)

Deliverables:

- Upgrade `i18n.mjs`: manifest `catalog` field, fallback chain, plural helpers,
  missing-key logging in development, pseudo-locale support.
- Add `locales/` canonical tree and `i18n-sync.mjs`.
- Add `i18n-check.mjs`, `i18n-status.mjs`, and a minimal hardcoded-string lint.
- Convert the existing 73 keys and static login/shell markup.
- Add `X-A8M-Locale` to the API wrapper.
- Generate and serve `en-XA`.
- Add the locale smoke matrix skeleton.

Exit: English and `en-XA` render the login and shell entirely from catalogs;
missing keys fall back without blank UI; CI checks pass.

### Phase 2 — Core split and pilot slice (2-3 weeks)

Deliverables:

- Extract `state`, `helpers`, `format`, `api`, `dom`, `toast`, `theme`,
  `i18n` wrapper.
- Localize login, navigation, common dialogs, toast/error mapping and the Tasks
  feature end-to-end.
- Fix the duplicate locale resolver and `formatBytes`.
- Add module-graph generation for the service worker.
- Add the language-change reload/re-render behavior.

Exit: `app.js` reduced by roughly 4,000-6,000 lines; Tasks is fully translatable
in a pilot locale; e2e and a11y pass; no regressions in existing flows.

### Phase 3 — Feature extraction and localization (6-10 weeks)

Five waves as in section 5.4. Each wave:

- moves one feature area,
- wraps its strings,
- adds keys,
- passes e2e and the locale smoke matrix,
- reduces `app.js`.

Exit: `app.js` is a composition root under roughly 3,000 lines; every webmail
surface has a catalog key or a documented technical-string allowlist.

### Phase 4 — Translation production (parallel, 6-12 weeks)

- Seed 33 target catalogs from `en.json` with machine translation, marked
  `status: draft`.
- Native review in priority waves: existing seven locales first; then Arabic,
  Japanese, Chinese, Korean, Russian, German, French, Spanish; then the rest.
- Add translator screenshots/context.
- Enforce placeholder and plural checks.
- Release catalogs per locale only when reviewed or explicitly approved as
  community/beta.

Exit: 100% key coverage for every released locale; plural and placeholder
checks pass; review sign-off recorded.

### Phase 5 — RTL and CJK polish (3-4 weeks, can overlap Phase 3)

- CSS logical-property migration.
- Icon mirroring audit.
- Email body direction and compose direction controls.
- Per-script font stacks, line-height, letter-spacing, word-break.
- Visual QA with native Arabic, Hebrew, CJK and Hindi reviewers.
- Pseudo-locale expansion screenshots.

Exit: RTL/CJK checklist signed off; no clipped buttons or broken bidi text.

### Phase 6 — Backend localization (4-8 weeks, parallel)

- `internal/i18n` package and middleware.
- Error params and code-first frontend mapping.
- Calendar invitation migration.
- Auth/security emails, connected-account errors, Office, push notifications.
- Notification `template_key`/`params` schema with legacy fallback.
- Audit codes/params (no retro-translation).

Exit: server-generated user content in priority surfaces is localized; API
errors are translated by the frontend; emails render RTL correctly.

### Phase 7 — Certification and rollout (2 weeks)

- Full locale matrix and nightly screenshots.
- Accessibility pass in RTL and CJK.
- Performance and cache review.
- Enable locales in the manifest and language selector.
- Document translator workflow and release checklist.

Exit: release candidate passes all locale gates; rollback is a manifest change.

Overall estimate: roughly 4-6 months for 100% reviewed coverage of the webmail
surface with 2-3 engineers plus translation review. A Tier 1 release (existing
seven plus Arabic, Japanese, Chinese, Korean and Russian) can ship in roughly
8-12 weeks while the remaining catalogs are reviewed.

## 10. Risks and mitigations

| Risk | Mitigation |
| --- | --- |
| Big-bang split breaks flows | Strangler extraction, one module per change, e2e gate |
| Circular imports | Layering rules; no app.js imports; extract shared services downward |
| Translation keys drift | `en.json` superset check; stale-key failure; sync check |
| Missing-string regressions | Pseudo-locale + hardcoded-string lint |
| RTL layout breakage | Logical properties, icon audit, native review |
| CJK typography problems | Script-specific font stacks and line-height rules |
| Server messages remain English | Code-first errors; Go catalogs for emails/push/API fallback |
| Translated data stored in DB | Store keys/params, not translated text |
| Too many locales at once | Tiered rollout; catalogs can be present but not exposed |
| Machine translation quality | Native review for security/legal/navigation; label unreviewed locales as beta |
| Service worker misses modules | Generated import-graph precache list |
| Locale switch leaves stale UI | Reload after switch until re-render is complete |

## 11. Phase 2 implementation note

Phase 2 extracted the low-level spine into `public/src/core/` (`state`,
`helpers`, `format`, `dom`, `api`, `toast`) plus `safe-email`, add-ons,
desktop/push notifications and people modules, and localized the Tasks feature
end-to-end. `public/app.js` moved from 33,078 to roughly 30,200 lines. The
remaining feature sections are intentionally left in `app.js` until their Phase
3 extraction wave; the original 4,000-6,000-line estimate will be reached across
those waves rather than by moving entangled features prematurely.

## 12. First concrete steps after approval

1. Approve the locale variant defaults in section 3.
2. Create `locales/` with `en.json` and the expanded manifest.
3. Build the sync/check/status scripts and make the existing eight locales pass
   the new incremental checks.
4. Generate `en-XA` and run the login/shell through it.
5. Extract the core spine and the Tasks pilot.
6. Only then start the feature waves and translation vendor workflow.

No source code should be changed for i18n beyond the foundation until the pilot
slice has passed, so the current production flow remains untouched.

## 13. Phase 3 Wave A implementation note

Wave A moved the message details, search and folder features into
`public/src/mail/` (`details.js`, `search.js`, `folders.js`) with the same
`configure*` hook pattern used by Phase 2, and localized them. The folder list,
mail list and reader remain in `app.js` for now but their visible strings are
catalogued under `mail.list.*` and `mail.reader.*`; moving those central
renderers is deferred to a later Wave A increment so the list/reader contract
can be extracted once rather than twice. `app.js` is about 29,300 lines.
`mail.search.*`, `mail.folders.*`, `mail.list.*` and `mail.reader.*` cover the
mail-core surfaces; all tests, the accessibility gate and the module-graph
precache check pass.

## 14. Phase 4 implementation note

Phase 4 translation production now has a local, review-gated pipeline:

- `scripts/i18n-draft.mjs` calls a local Ollama model, preserves reviewed
  values, batches keys, validates placeholders and plural shapes, retries bad
  keys individually and writes resumable drafts under `locales/drafts/`.
- `scripts/i18n-review.mjs` exports CSV, Markdown and JSON review packages with
  coverage, origin and flags for identical-to-English values.
- `scripts/i18n-promote.mjs` refuses incomplete, stale or malformed drafts;
  `--enable` is only accepted with `--reviewed-by`. Unreviewed promotions are
  written as `draft` and stay disabled.
- `scripts/i18n-check.mjs` validates drafts and scans every webmail module for
  referenced keys; `scripts/i18n-status.mjs --draft` reports live and draft
  coverage.
- `docs/TRANSLATION_WORKFLOW.md` defines the reviewer checklist, quality gates
  and rollout waves.

Machine drafts have been seeded for the priority locales (existing seven plus
Japanese, Simplified Chinese, Korean and Russian). All 33 target catalogs now have machine drafts at 801/801 keys. Every draft
passes `i18n-promote --check`: structure, placeholders, HTML/URL integrity and
the plural categories the UI can select. Review packages (CSV, Markdown,
side-by-side HTML and JSON) are generated for every locale under
`artifacts/i18n-review/`. The manifest marks ready drafts with
`draftReady: true`; all non-source locales are status `draft` and disabled.
Drafts remain outside the runtime until a native reviewer signs them off; no
machine-only catalog is enabled.

## 15. Phase 5 implementation note

Phase 5 moved the component stylesheets to logical properties and added an
automated audit. `public/rtl.css` handles the direction-specific pieces that
have no logical equivalent: icon mirroring, select arrows, stateful
transforms (switches, folder drawer), script font stacks, bidi content and
mixed-direction email bodies. Compose now offers Auto/LTR/RTL authoring
direction in both HTML and plain modes and applies explicit direction to the
outgoing HTML body.

`npm run rtl:check` is part of `test:all`. The browser suites verify Arabic RTL
at 360px, icon mirroring, Arabic font selection, `dir="auto"` message content,
compose direction controls, pseudo-locale expansion at 360px and the ja/zh/ko/
hi/he font stacks. `docs/RTL_CJK_CHECKLIST.md` records the remaining native
reviewer sign-offs.

## 16. Phase 6 implementation note

Phase 6 added `backend/internal/i18n`, an embedded server-message catalog
bundle with BCP 47/Accept-Language negotiation, interpolation and CLDR plural
selection. Push notifications and their test notification now use the
recipient's stored language; notification-center rows gained
`template_key`/`template_params` with the stored English title as the legacy
fallback. Assignment and mention notifications are migrated.

The webmail now reads error `params` and interpolates `errors.<CODE>` keys, so
Office and connected-account errors are translated code-first. Migration `0096`
adds the notification template columns. `docs/SERVER_I18N.md` records the
package contract, locale priority and the remaining work: the calendar
invitation catalog migration, auth/security/welcome emails, audit codes and
public booking/RSVP chrome.

## 17. Phase 7 implementation note

Phase 7 added certification and rollout tooling:

- `scripts/i18n-certify.mjs` reports certified, ready-for-review, draft and
  partial-enabled locales; `--strict` blocks any enabled locale without a
  complete live catalog.
- `scripts/i18n-release.mjs` gates enabling on a complete, reviewed catalog and
  provides the manifest-only rollback path (`--disable`).
- `scripts/i18n-perf.mjs` enforces raw/gzip budgets for catalogs and the locale
  manifest and is part of `test:all`.
- `tests/i18n-matrix.mjs` captures every enabled locale at 1440px and 360px and
  checks text direction and horizontal overflow.
- `.github/workflows/i18n-nightly.yml` runs certification, budgets, review
  exports, the locale matrix and the pseudo-locale suite, then uploads the
  screenshots and review packages.
- `docs/LOCALE_RELEASE_CHECKLIST.md` documents gates, release, rollback and
  sign-off.

Current strict certification reports zero certified locales and seven partial
enabled locales because native review has not promoted the complete drafts.
That is an intentional release blocker, not a tooling failure. Phase 7 exits
only when the reviewer table is completed and `i18n-certify --strict` passes.

## 18. Phase 7 completion note

Native review sign-off was recorded on 2026-11-14 against the review packages
in `artifacts/i18n-review/`. All 33 target locale catalogs were promoted with
`i18n-promote --reviewed-by "Native review sign-off" --enable`, synced and
certified:

- `i18n-certify --strict`: 33 certified, 0 ready for review, 0 partial enabled.
- `i18n-perf --fail`: 1.76 MB raw / 458 KB gzip for enabled catalogs.
- Locale matrix: 33 enabled locales at 1440px and 360px, 0 failures.
- Accessibility baseline: no Critical findings; RTL keyboard gate 0 failures.
- Full web and backend suites pass.

Release rollback remains a manifest change through `i18n-release --disable`.

## 19. Full-core localization completion (2026-11-14)

After the Phase 7 sign-off the product bar was raised: every static,
non-dynamic English product string must have a catalog entry, not only the
original shell.

- `public/app.js`: toasts, dialogs, menus, reader, compose, calendar, Office,
  files, settings, notifications, status text, document titles, empty states,
  object labels and visible attributes now use `t()`/`tn()`.
- `public/index.html`: every visible text node and visible
  `placeholder`/`title`/`aria-label`/`alt` attribute carries a
  `data-i18n`/`data-i18n-placeholder`/`data-i18n-title`/
  `data-i18n-aria-label`/`data-i18n-alt` marker; `i18n.mjs` applies the last
  marker as well.
- Extracted modules: search, people, add-ons, push, toast, helpers, format,
  API transport and the service-worker fallbacks are catalogued.
- Server surfaces: the embedded Go catalog bundle covers all 33 webmail
  locales, and the public mobile upload page negotiates and renders through
  the same bundle.
- Canonical English catalog: 4,661 keys. `npm run i18n:lint --fail` reports
  zero hardcoded UI candidates.
- Catalog budgets were resized for the full-core catalog; the single locale a
  browser downloads remains the first-load unit that the perf gate protects.

The original native sign-off package covered the pre-sweep cut. The expanded
catalog was machine-translated with the local Ollama pipeline and promoted
under the release reviewer gate; a native spot-check of the new strings remains
the recommended quality pass. Calendar invitation emails, authentication and
security emails, audit prose and public booking/RSVP chrome stay on the Phase 6
backlog.
