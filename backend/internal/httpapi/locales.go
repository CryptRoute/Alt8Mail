package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	serveri18n "github.com/CryptRoute/Alt8Mail/backend/internal/i18n"
	weblocales "github.com/CryptRoute/Alt8Mail/backend/internal/locales"
	"github.com/jackc/pgx/v5"
)

const localePolicyTTL = 30 * time.Second

// publicLocales returns the webmail manifest with the Control Plane's
// enabled/disabled policy applied. It is deliberately unauthenticated: the
// sign-in screen needs the same language list as the signed-in workspace.
func (s *Server) publicLocales(w http.ResponseWriter, r *http.Request) {
	manifest := weblocales.LoadOrDefault(s.cfg.PublicDir)
	response := manifest.Resolve(s.webmailLocalePolicyFresh(r.Context()))
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, response)
}

// adminLocales is the same contract for the customer administration console.
// It is authenticated by the admin middleware even though the payload is not
// sensitive.
func (s *Server) adminLocales(w http.ResponseWriter, r *http.Request) {
	s.publicLocales(w, r)
}

// webmailLocalePolicy reads the Control Plane's single-row language policy.
// A missing table (a host in the middle of an update) or a database outage is
// treated as "all languages enabled", never as "only English".
func (s *Server) webmailLocalePolicy(ctx context.Context) weblocales.Policy {
	if s.localePolicyLoader != nil {
		if policy, err := s.localePolicyLoader(ctx); err == nil {
			return policy
		}
	}
	s.localePolicyMu.Lock()
	if !s.localePolicyAt.IsZero() && time.Since(s.localePolicyAt) < localePolicyTTL {
		policy := s.localePolicyCache
		s.localePolicyMu.Unlock()
		return policy
	}
	s.localePolicyMu.Unlock()

	policy, err := s.loadWebmailLocalePolicy(ctx)
	if err != nil && s.logger != nil {
		s.logger.Warn("webmail language policy unavailable; treating all locales as enabled", "err", err)
	}
	if err != nil {
		policy = weblocales.Policy{}
	}
	s.localePolicyMu.Lock()
	s.localePolicyCache = policy
	s.localePolicyAt = time.Now()
	s.localePolicyMu.Unlock()
	return policy
}

// webmailLocalePolicyFresh bypasses the short request-path cache so an
// operator's save is visible on the very next page load.
func (s *Server) webmailLocalePolicyFresh(ctx context.Context) weblocales.Policy {
	if s.localePolicyLoader != nil {
		if policy, err := s.localePolicyLoader(ctx); err == nil {
			return policy
		}
	}
	policy, err := s.loadWebmailLocalePolicy(ctx)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("webmail language policy unavailable; treating all locales as enabled", "err", err)
		}
		return weblocales.Policy{}
	}
	return policy
}

func (s *Server) loadWebmailLocalePolicy(ctx context.Context) (weblocales.Policy, error) {
	if s.store == nil || s.store.DB == nil {
		return weblocales.Policy{}, nil
	}
	var defaultLocale string
	var raw []byte
	err := s.store.DB.QueryRow(ctx, `SELECT default_locale, disabled FROM control.webmail_locales WHERE singleton`).Scan(&defaultLocale, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return weblocales.Policy{}, nil
	}
	if err != nil {
		return weblocales.Policy{}, err
	}
	policy := weblocales.Policy{Default: strings.TrimSpace(defaultLocale)}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &policy.Disabled)
	}
	return policy, nil
}

// applyEffectiveLanguage gives users without an explicit, enabled language the
// system default selected in the Control Plane. This is what makes the setting
// apply to webmail, admin and Control Plane clients by default.
func (s *Server) applyEffectiveLanguage(ctx context.Context, user userRecord, prefs map[string]any) map[string]any {
	if prefs == nil {
		prefs = map[string]any{}
	}
	explicit := preferenceLanguage(user.Preferences)
	if explicit != "" && s.webmailLanguageEnabled(ctx, explicit) {
		prefs["language"] = explicit
		return prefs
	}
	prefs["language"] = s.webmailDefaultLocale(ctx)
	return prefs
}

// webmailDefaultLocale returns the configured default only when it is still a
// shipped, enabled locale; otherwise English is the safe answer.
func (s *Server) webmailDefaultLocale(ctx context.Context) string {
	manifest := weblocales.LoadOrDefault(s.cfg.PublicDir)
	return manifest.Resolve(s.webmailLocalePolicyFresh(ctx)).Default
}

// webmailLanguageEnabled is the single check used when persisting a user
// preference, onboarding or an administrator's default-language setting.
func (s *Server) webmailLanguageEnabled(ctx context.Context, code string) bool {
	code = strings.TrimSpace(code)
	if code == "" {
		return false
	}
	manifest := weblocales.LoadOrDefault(s.cfg.PublicDir)
	entry, ok := manifest.Entry(code)
	if !ok || entry.IsPseudo() || entry.Hidden || !entry.IsEnabled() {
		return false
	}
	if strings.EqualFold(entry.Code, weblocales.DefaultCode) {
		return true
	}
	for _, disabled := range s.webmailLocalePolicyFresh(ctx).Disabled {
		if strings.EqualFold(strings.TrimSpace(disabled), entry.Code) {
			return false
		}
	}
	return true
}

// enforceEnabledTranslator keeps server-rendered pages (mobile upload, web
// push) out of a language the operator has disabled, including when the
// browser's Accept-Language header asks for it.
func (s *Server) enforceEnabledTranslator(ctx context.Context, translator *serveri18n.Translator) *serveri18n.Translator {
	if translator == nil {
		return nil
	}
	code := weblocales.CatalogLanguage(translator.Locale())
	if code == "" || s.webmailLanguageEnabled(ctx, code) {
		return translator
	}
	if s.i18n == nil {
		return translator
	}
	return s.i18n.Translator(weblocales.DefaultCode)
}
