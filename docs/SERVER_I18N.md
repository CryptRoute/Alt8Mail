# Server-side localization

Status: Phase 6 foundation, full-core catalog coverage and the public mobile
upload page localized.
Date: 2026-11-14.

The webmail catalog translates the interface in the browser. Server-generated
content — push notifications, notification-center rows and feature emails — is
translated in Go through `backend/internal/i18n`, because it is produced when no
web page is open.

## Package

`backend/internal/i18n` embeds `catalogs/*.json` and exposes:

- `Bundle.Match(preferred ...string)` — BCP 47 and `Accept-Language` matching,
  English fallback, regional aliases (`de-DE` → `de`).
- `Translator.T(key, params)` — string lookup with `{placeholder}` interpolation.
- `Translator.N(key, count, params)` — CLDR cardinal plural selection via
  `golang.org/x/text/feature/plural`.
- `Translator.Dir()` — `rtl` for Arabic, Hebrew, Farsi and Urdu.

Server catalogs ship for English and all 33 webmail target locales. The
catalogs are machine-translated with the same local Ollama pipeline as the
webmail catalogs and follow the same manifest review gate; a missing server key
still falls back to English at runtime so a partial catalog never blanks a
server message.

The package tests enforce:

- every catalog has the same keys as English;
- placeholders match the English source (Arabic zero/dual forms may omit
  `{count}`);
- plural selection for English, Russian, Arabic and Japanese;
- exact, base-language and loose `Accept-Language` negotiation.

## Locale priority

1. `X-A8M-Locale` sent by the webmail.
2. The session user's stored `language` preference.
3. `Accept-Language`.
4. English.

Push delivery has no request, so it reads the recipient's stored preference and
falls back to English.

## Current server surfaces

| Surface | Status |
| --- | --- |
| Push: new message, multiple messages, account label, test notification | Localized in all 33 server catalogs |
| Public mobile upload page | Template, expiry, session and upload states localized through `requestTranslator` |
| Notification center: assignment and mention titles | `template_key` + `template_params`; legacy rows keep stored English |
| API errors: Office and connected-account codes | Code-first in the webmail (`errors.OFFICE_*`, `errors.ACCOUNT_*`), with `params` support |
| Calendar invitations | Existing dedicated catalog for eight languages; migration to this package is the next increment |
| Auth/security and welcome emails | Not migrated yet; still English templates |
| Audit records | Still store English descriptions; code/params migration is pending |

## Database

Migration `0096_notification_templates` adds `template_key` and
`template_params jsonb` to `mail_notifications`. New rows write both the
localized-at-read key and the original English title/body. The notification
loader translates the key for the signed-in reader; rows without a key keep
their stored title, so no backfill is required.

## Adding a server string

1. Add the key to `internal/i18n/catalogs/en.json` and every other catalog.
   Use `server.<surface>.<name>` and keep `{placeholders}` identical.
2. Add or update a test in `internal/i18n/i18n_test.go`.
3. Resolve the translator from the request (`requestTranslator`) or the user
   (`translatorForUser`) and call `T`/`N`, with the existing English text as the
   fallback.
4. Run `go test ./internal/i18n ./internal/httpapi`.

Server catalogs are intentionally separate from the webmail catalogs: they
contain only content that is rendered without a browser and can ship
independently of the interface translation review.

## Remaining Phase 6 work

- Move `calendarInviteCatalog` onto `internal/i18n` keys and extend invitations
  to all released locales.
- Localize authentication, security-alert and welcome emails, including the
  recipient's preferred language when known.
- Replace remaining connected-account and Office server strings with stable
  codes plus params.
- Add audit `code`/`params` columns and stop writing translated prose into
  audit records.
- Localize public booking and RSVP page chrome, not only invitation emails.
