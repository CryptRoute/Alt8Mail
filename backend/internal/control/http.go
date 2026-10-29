package control

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type apiFailure struct {
	Status  int
	Code    string
	Message string
	Details any
}

func (e *apiFailure) Error() string { return e.Message }

func fail(status int, code, message string) error {
	return &apiFailure{Status: status, Code: code, Message: message}
}

func failDetails(status int, code, message string, details any) error {
	return &apiFailure{Status: status, Code: code, Message: message, Details: details}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message}})
}

func writeFromError(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "That record no longer exists")
		return
	}
	var failure *apiFailure
	if errors.As(err, &failure) {
		body := map[string]any{"code": failure.Code, "message": failure.Message}
		if failure.Details != nil {
			body["details"] = failure.Details
		}
		writeJSON(w, failure.Status, map[string]any{"error": body})
		return
	}
	writeError(w, http.StatusInternalServerError, "SERVER", "The control service could not complete that operation")
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any, limit int64) error {
	if limit <= 0 {
		limit = 2 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("request body is required")
		}
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

func clientIP(r *http.Request, trustedProxy bool) net.IP {
	if trustedProxy {
		if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" {
			if parsed := net.ParseIP(forwarded); parsed != nil {
				return parsed
			}
		}
		if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
			if parsed := net.ParseIP(real); parsed != nil {
				return parsed
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(strings.Trim(host, "[]"))
}

func (s *Server) requestIP(r *http.Request) net.IP {
	return clientIP(r, s.cfg.TrustedProxy)
}

func userAgent(r *http.Request) string {
	value := strings.TrimSpace(r.UserAgent())
	if len(value) > 500 {
		value = value[:500]
	}
	return value
}

func (s *Server) audit(ctx context.Context, user *adminUser, action, target, detail string, ip net.IP, ua string, metadata any) {
	if metadata == nil {
		metadata = map[string]any{}
	}
	raw, _ := json.Marshal(metadata)
	var actorID any
	actorEmail := ""
	if user != nil {
		actorID = user.ID
		actorEmail = user.Email
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO control.audit_log (actor_id, actor_email, action, target, detail, ip, user_agent, metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb)
	`, actorID, actorEmail, action, target, detail, nullableIP(ip), ua, raw)
	if err != nil && s.logger != nil {
		s.logger.Warn("control audit write failed", "action", action, "err", err)
	}
}

func nullableIP(ip net.IP) any {
	if ip == nil {
		return nil
	}
	return ip.String()
}

func cookie(r *http.Request, name string) string {
	value, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return value.Value
}

func setCookie(w http.ResponseWriter, name, value string, maxAge int, httpOnly bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		Expires:  time.Now().Add(time.Duration(maxAge) * time.Second),
		HttpOnly: httpOnly,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

func clearCookie(w http.ResponseWriter, name string, httpOnly bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
		HttpOnly: httpOnly,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) signCSRF(token string) string {
	mac := hmac.New(sha256.New, []byte(s.cfg.CSRFSecret))
	mac.Write([]byte(token))
	return token + "." + hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) validSignedCSRF(value string) (string, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || parts[0] == "" {
		return "", false
	}
	expected := s.signCSRF(parts[0])
	return parts[0], hmac.Equal([]byte(expected), []byte(value))
}

func (s *Server) ensureCSRFCookie(w http.ResponseWriter, r *http.Request) string {
	if existing := cookie(r, csrfCookieName); existing != "" {
		if _, ok := s.validSignedCSRF(existing); ok {
			return existing
		}
	}
	token, err := randomToken(24)
	if err != nil {
		return ""
	}
	value := s.signCSRF(token)
	setCookie(w, csrfCookieName, value, 12*3600, false)
	return value
}

func (s *Server) validateCSRF(r *http.Request, sess *sessionRecord) bool {
	if sess == nil {
		cookieValue := cookie(r, csrfCookieName)
		base, ok := s.validSignedCSRF(cookieValue)
		if !ok {
			return false
		}
		header := r.Header.Get("X-Alt8Mail-CSRF")
		if !constantTimeEqual(header, cookieValue) {
			return false
		}
		_ = base
		return true
	}
	cookieValue := cookie(r, csrfCookieName)
	base, ok := s.validSignedCSRF(cookieValue)
	if !ok || !constantTimeEqual(base, sess.CSRF) {
		return false
	}
	header := r.Header.Get("X-Alt8Mail-CSRF")
	return constantTimeEqual(header, cookieValue)
}

type authedHandler func(http.ResponseWriter, *http.Request, *sessionRecord)

func (s *Server) require(permission string, next authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.authenticateSession(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Sign in to System Control")
			return
		}
		if permission != "" && !sess.User.PermSet.Has(permission) {
			writeError(w, http.StatusForbidden, "FORBIDDEN", "Your panel role does not allow this action")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !s.validateCSRF(r, sess) {
			writeError(w, http.StatusForbidden, "CSRF", "Missing or invalid CSRF token")
			return
		}
		next(w, r, sess)
	}
}

func (s *Server) requireAny(permissions []string, next authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.authenticateSession(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Sign in to System Control")
			return
		}
		allowed := len(permissions) == 0
		for _, permission := range permissions {
			if sess.User.PermSet.Has(permission) {
				allowed = true
				break
			}
		}
		if !allowed {
			writeError(w, http.StatusForbidden, "FORBIDDEN", "Your panel role does not allow this action")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !s.validateCSRF(r, sess) {
			writeError(w, http.StatusForbidden, "CSRF", "Missing or invalid CSRF token")
			return
		}
		next(w, r, sess)
	}
}

func pathID(r *http.Request) string {
	return strings.TrimSpace(r.PathValue("id"))
}

func queryInt(r *http.Request, key string, fallback, min, max int) int {
	value, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get(key)))
	if err != nil {
		return fallback
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func queryBool(r *http.Request, key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key)))
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func (s *Server) actorAudit(r *http.Request, sess *sessionRecord, action, target, detail string, metadata any) {
	if sess == nil {
		return
	}
	s.audit(r.Context(), &sess.User, action, target, detail, s.requestIP(r), userAgent(r), metadata)
}
