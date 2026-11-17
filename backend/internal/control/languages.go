package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	weblocales "github.com/CryptRoute/Alt8Mail/backend/internal/locales"
	"github.com/jackc/pgx/v5"
)

// webmailLocaleConfig is the operator-managed language policy. The shipped
// manifest remains the source of truth for which locales exist.
type webmailLocaleConfig struct {
	Default  string
	Disabled []string
}

func (s *Server) registerLanguageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/languages", s.require("settings.write", s.handleLanguages))
	mux.HandleFunc("PUT /control/api/languages", s.require("settings.write", s.handleSaveLanguages))
	mux.HandleFunc("GET /control/api/locales", s.handlePublicLocales)
	mux.HandleFunc("PATCH /control/api/me/language", s.require("", s.handleOwnLanguage))
}

// handlePublicLocales lets the sign-in screen load the enabled language list
// and the system default before an operator has authenticated.
func (s *Server) handlePublicLocales(w http.ResponseWriter, r *http.Request) {
	config, err := s.loadWebmailLocaleConfig(r.Context())
	if err != nil && s.logger != nil {
		s.logger.Warn("webmail language policy load failed", "err", err)
	}
	response := s.webmailManifest().Resolve(weblocales.Policy{Default: config.Default, Disabled: config.Disabled})
	writeJSON(w, http.StatusOK, response)
}

// handleOwnLanguage stores an operator's explicit language. An empty value
// means "follow the system default".
func (s *Server) handleOwnLanguage(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Language string `json:"language"`
	}
	if err := decodeJSON(w, r, &body, 32*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	language := strings.TrimSpace(body.Language)
	if language != "" && !s.languageAvailable(r.Context(), language) {
		writeError(w, http.StatusBadRequest, "LANGUAGE", "Choose an enabled language")
		return
	}
	if _, err := s.db.Exec(r.Context(), `
		UPDATE control.admin_users SET language=$2, updated_at=now() WHERE id=$1
	`, sess.User.ID, language); err != nil {
		writeFromError(w, err)
		return
	}
	sess.User.Language = language
	s.audit(r.Context(), &sess.User, "language.updated", sess.User.Email,
		"Updated the Control Plane language", s.requestIP(r), userAgent(r), map[string]any{"language": language})
	writeJSON(w, http.StatusOK, map[string]any{"language": language})
}

func (s *Server) languageAvailable(ctx context.Context, code string) bool {
	entry, ok := s.webmailManifest().Entry(code)
	if !ok || entry.IsPseudo() || entry.Hidden || !entry.IsEnabled() {
		return false
	}
	config, err := s.loadWebmailLocaleConfig(ctx)
	if err != nil {
		return true
	}
	for _, disabled := range config.Disabled {
		if strings.EqualFold(strings.TrimSpace(disabled), entry.Code) {
			return false
		}
	}
	return true
}

// webmailManifest reads the release manifest from the web root. The compiled
// fallback keeps the operator picker usable on a partially updated host.
func (s *Server) webmailManifest() weblocales.Manifest {
	return weblocales.LoadOrDefault(s.cfg.WebRoot)
}

func (s *Server) handleLanguages(w http.ResponseWriter, r *http.Request, _ *sessionRecord) {
	config, err := s.loadWebmailLocaleConfig(r.Context())
	if err != nil && s.logger != nil {
		s.logger.Warn("webmail language policy load failed", "err", err)
	}
	manifest := s.webmailManifest()
	response := manifest.Resolve(weblocales.Policy{Default: config.Default, Disabled: config.Disabled})
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleSaveLanguages(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Disabled []string `json:"disabled"`
		Default  string   `json:"default"`
	}
	if err := decodeJSON(w, r, &body, 64*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	manifest := s.webmailManifest()
	current, err := s.loadWebmailLocaleConfig(r.Context())
	if err != nil && s.logger != nil {
		s.logger.Warn("webmail language policy load failed", "err", err)
	}
	defaultCode := strings.TrimSpace(body.Default)
	if defaultCode == "" {
		defaultCode = current.Default
	}
	defaultCode, err = manifest.NormalizeDefault(defaultCode)
	if err != nil {
		writeError(w, http.StatusBadRequest, "LANGUAGES", err.Error())
		return
	}
	disabled, err := manifest.NormalizeDisabled(body.Disabled, defaultCode)
	if err != nil {
		writeError(w, http.StatusBadRequest, "LANGUAGES", err.Error())
		return
	}
	config := webmailLocaleConfig{Default: defaultCode, Disabled: disabled}
	if err := s.saveWebmailLocaleConfig(r.Context(), config, sess.User.ID); err != nil {
		if s.logger != nil {
			s.logger.Error("webmail language policy save failed", "err", err)
		}
		writeError(w, http.StatusInternalServerError, "LANGUAGES", "Could not save the language policy")
		return
	}
	s.audit(r.Context(), &sess.User, "languages.update", "webmail",
		fmt.Sprintf("Language policy updated · %d disabled · default %s", len(disabled), defaultCode),
		s.requestIP(r), userAgent(r), map[string]any{"default": defaultCode, "disabled": disabled})
	writeJSON(w, http.StatusOK, manifest.Resolve(weblocales.Policy{Default: defaultCode, Disabled: disabled}))
}

func (s *Server) loadWebmailLocaleConfig(ctx context.Context) (webmailLocaleConfig, error) {
	if s.localeConfigLoader != nil {
		return s.localeConfigLoader(ctx)
	}
	config := webmailLocaleConfig{Default: weblocales.DefaultCode}
	if s.db == nil {
		return config, nil
	}
	var raw []byte
	err := s.db.QueryRow(ctx, `
		SELECT default_locale, disabled
		FROM control.webmail_locales
		WHERE singleton
	`).Scan(&config.Default, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return webmailLocaleConfig{Default: weblocales.DefaultCode}, nil
	}
	if err != nil {
		return config, err
	}
	config.Default = strings.TrimSpace(config.Default)
	if config.Default == "" {
		config.Default = weblocales.DefaultCode
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &config.Disabled)
	}
	return config, nil
}

func (s *Server) saveWebmailLocaleConfig(ctx context.Context, config webmailLocaleConfig, userID string) error {
	if s.localeConfigSaver != nil {
		return s.localeConfigSaver(ctx, config, userID)
	}
	if s.db == nil {
		return errors.New("control database is not configured")
	}
	raw, err := json.Marshal(config.Disabled)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO control.webmail_locales (singleton, default_locale, disabled, updated_by, updated_at)
		VALUES (true, $1, $2::jsonb, NULLIF($3::text, '')::uuid, now())
		ON CONFLICT (singleton) DO UPDATE SET
			default_locale = EXCLUDED.default_locale,
			disabled = EXCLUDED.disabled,
			updated_by = EXCLUDED.updated_by,
			updated_at = now()
	`, config.Default, raw, userID)
	return err
}
