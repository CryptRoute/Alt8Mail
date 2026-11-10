package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type siemConfig struct {
	TenantID           string
	Endpoint           string
	SecretSealed       string
	Enabled            bool
	IncludeUserEvents  bool
	IncludeAdminEvents bool
	LastUserAuditID    int64
	LastAdminAuditID   int64
}

func validateSIEMEndpoint(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("Enter the HTTPS endpoint that should receive audit events")
	}
	if len(raw) > 2048 {
		return nil, fmt.Errorf("The endpoint URL is too long")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("Enter a valid endpoint URL")
	}
	if parsed.Scheme != "https" {
		return nil, fmt.Errorf("SIEM endpoints must use HTTPS")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("SIEM endpoints cannot contain credentials")
	}
	if parsed.Fragment != "" {
		return nil, fmt.Errorf("SIEM endpoints cannot contain a fragment")
	}
	port := parsed.Port()
	if port == "" {
		port = "443"
	}
	if port != "443" {
		return nil, fmt.Errorf("SIEM endpoints must use port 443")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil, fmt.Errorf("Local SIEM endpoints are not allowed")
	}
	if literal := net.ParseIP(host); literal != nil && !publicRemoteIP(literal) {
		return nil, fmt.Errorf("Private SIEM endpoints are not allowed")
	}
	return parsed, nil
}

func siemHTTPClient() *http.Client {
	resolver := net.DefaultResolver
	dialer := &net.Dialer{Timeout: 8 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, splitErr := net.SplitHostPort(address)
			if splitErr != nil {
				return nil, splitErr
			}
			ips, lookupErr := resolver.LookupIPAddr(ctx, host)
			if lookupErr != nil {
				return nil, lookupErr
			}
			for _, candidate := range ips {
				if !publicRemoteIP(candidate.IP) {
					continue
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
			}
			return nil, fmt.Errorf("SIEM endpoint did not resolve to a public address")
		},
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   8 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return fmt.Errorf("SIEM endpoints may not redirect")
		},
	}
}

func signSIEMPayload(secret string, timestamp int64, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	mac.Write([]byte("."))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func deliverSIEMEvent(ctx context.Context, endpoint, secret, deliveryID, source string, payload []byte) (int, error) {
	parsed, err := validateSIEMEndpoint(endpoint)
	if err != nil {
		return 0, err
	}
	timestamp := time.Now().Unix()
	signature := signSIEMPayload(secret, timestamp, payload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Alt8Mail-SIEM/1.0")
	request.Header.Set("X-A8M-Delivery", deliveryID)
	request.Header.Set("X-A8M-Source", source)
	request.Header.Set("X-A8M-Timestamp", strconv.FormatInt(timestamp, 10))
	request.Header.Set("X-A8M-Signature", "sha256="+signature)
	client := siemHTTPClient()
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("deliver webhook: %w", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		excerpt := strings.TrimSpace(string(body))
		if excerpt != "" {
			return response.StatusCode, fmt.Errorf("SIEM endpoint returned %d: %s", response.StatusCode, truncate(excerpt, 300))
		}
		return response.StatusCode, fmt.Errorf("SIEM endpoint returned %d", response.StatusCode)
	}
	return response.StatusCode, nil
}

func (s *Server) loadSIEMConfig(ctx context.Context, tenantID string) siemConfig {
	config := siemConfig{TenantID: tenantID, IncludeUserEvents: true, IncludeAdminEvents: true}
	_ = s.store.DB.QueryRow(ctx, `
		SELECT endpoint, secret_sealed, enabled, include_user_events, include_admin_events,
		       last_user_audit_id, last_admin_audit_id
		FROM public.tenant_siem_config WHERE tenant_id=$1
	`, tenantID).Scan(&config.Endpoint, &config.SecretSealed, &config.Enabled, &config.IncludeUserEvents,
		&config.IncludeAdminEvents, &config.LastUserAuditID, &config.LastAdminAuditID)
	return config
}

func (s *Server) adminGetSIEM(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	config := s.loadSIEMConfig(r.Context(), sess.ActiveTenantID)
	var lastDelivery *time.Time
	var lastError string
	var updatedAt *time.Time
	_ = s.store.DB.QueryRow(r.Context(), `
		SELECT last_delivery_at, last_error, updated_at FROM public.tenant_siem_config WHERE tenant_id=$1
	`, sess.ActiveTenantID).Scan(&lastDelivery, &lastError, &updatedAt)
	pending, delivered, failed := 0, 0, 0
	_ = s.store.DB.QueryRow(r.Context(), `
		SELECT count(*) FILTER (WHERE status='pending'),
		       count(*) FILTER (WHERE status='delivered'),
		       count(*) FILTER (WHERE status='failed')
		FROM public.tenant_siem_deliveries WHERE tenant_id=$1
	`, sess.ActiveTenantID).Scan(&pending, &delivered, &failed)
	var lastDeliveryValue, updatedValue any
	if lastDelivery != nil {
		lastDeliveryValue = lastDelivery.UTC().Format(time.RFC3339)
	}
	if updatedAt != nil {
		updatedValue = updatedAt.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": config.Endpoint != "", "endpoint": config.Endpoint,
		"enabled": config.Enabled, "hasSecret": config.SecretSealed != "",
		"includeUserEvents": config.IncludeUserEvents, "includeAdminEvents": config.IncludeAdminEvents,
		"lastDeliveryAt": lastDeliveryValue, "lastError": lastError, "updatedAt": updatedValue,
		"deliveries": map[string]any{"pending": pending, "delivered": delivered, "failed": failed},
	})
}

func newSIEMSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func (s *Server) adminPutSIEM(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		Endpoint           string `json:"endpoint"`
		Enabled            *bool  `json:"enabled"`
		IncludeUserEvents  *bool  `json:"includeUserEvents"`
		IncludeAdminEvents *bool  `json:"includeAdminEvents"`
		Secret             string `json:"secret"`
		RotateSecret       bool   `json:"rotateSecret"`
	}
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	endpoint := strings.TrimSpace(body.Endpoint)
	if endpoint != "" {
		if _, err := validateSIEMEndpoint(endpoint); err != nil {
			writeError(w, http.StatusBadRequest, "SIEM_ENDPOINT", err.Error())
			return
		}
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	if enabled && endpoint == "" {
		writeError(w, http.StatusBadRequest, "SIEM_ENDPOINT", "Add an HTTPS endpoint before enabling the integration")
		return
	}
	includeUser, includeAdmin := true, true
	if body.IncludeUserEvents != nil {
		includeUser = *body.IncludeUserEvents
	}
	if body.IncludeAdminEvents != nil {
		includeAdmin = *body.IncludeAdminEvents
	}
	var existingSealed, existingEndpoint string
	existingErr := s.store.DB.QueryRow(r.Context(), `
		SELECT secret_sealed, endpoint FROM public.tenant_siem_config WHERE tenant_id=$1
	`, sess.ActiveTenantID).Scan(&existingSealed, &existingEndpoint)
	hasExisting := existingErr == nil
	sealed := existingSealed
	plaintextSecret := ""
	if strings.TrimSpace(body.Secret) != "" {
		plaintextSecret = strings.TrimSpace(body.Secret)
		if len(plaintextSecret) < 16 || len(plaintextSecret) > 256 {
			writeError(w, http.StatusBadRequest, "SIEM_SECRET", "The signing secret must be between 16 and 256 characters")
			return
		}
		encoded, err := s.sealSecuritySecret(plaintextSecret)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "SIEM_SECRET", "Could not protect the signing secret")
			return
		}
		sealed = encoded
		plaintextSecret = ""
	} else if sealed == "" || body.RotateSecret {
		generated, err := newSIEMSecret()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "SIEM_SECRET", "Could not generate a signing secret")
			return
		}
		encoded, err := s.sealSecuritySecret(generated)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "SIEM_SECRET", "Could not protect the signing secret")
			return
		}
		sealed = encoded
		plaintextSecret = generated
	}
	seedCursors := endpoint != "" && (!hasExisting || existingEndpoint == "")
	lastUser, lastAdmin := int64(0), int64(0)
	if seedCursors {
		_ = s.store.DB.QueryRow(r.Context(), `SELECT COALESCE(max(id),0) FROM public.audit_log WHERE tenant_id=$1`, sess.ActiveTenantID).Scan(&lastUser)
		_ = s.store.DB.QueryRow(r.Context(), `SELECT COALESCE(max(id),0) FROM public.admin_audit_log WHERE tenant_id=$1`, sess.ActiveTenantID).Scan(&lastAdmin)
	}
	if !hasExisting {
		_, err := s.store.DB.Exec(r.Context(), `
			INSERT INTO public.tenant_siem_config
			  (tenant_id, endpoint, secret_sealed, enabled, include_user_events, include_admin_events,
			   last_user_audit_id, last_admin_audit_id, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now())
		`, sess.ActiveTenantID, endpoint, sealed, enabled, includeUser, includeAdmin, lastUser, lastAdmin)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "SIEM", "Could not save the SIEM integration")
			return
		}
	} else if seedCursors {
		_, err := s.store.DB.Exec(r.Context(), `
			UPDATE public.tenant_siem_config
			SET endpoint=$2, secret_sealed=$3, enabled=$4, include_user_events=$5,
			    include_admin_events=$6, last_user_audit_id=$7, last_admin_audit_id=$8, updated_at=now()
			WHERE tenant_id=$1
		`, sess.ActiveTenantID, endpoint, sealed, enabled, includeUser, includeAdmin, lastUser, lastAdmin)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "SIEM", "Could not save the SIEM integration")
			return
		}
	} else {
		_, err := s.store.DB.Exec(r.Context(), `
			UPDATE public.tenant_siem_config
			SET endpoint=$2, secret_sealed=$3, enabled=$4, include_user_events=$5,
			    include_admin_events=$6, updated_at=now()
			WHERE tenant_id=$1
		`, sess.ActiveTenantID, endpoint, sealed, enabled, includeUser, includeAdmin)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "SIEM", "Could not save the SIEM integration")
			return
		}
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.siem_updated", endpoint, "Updated SIEM audit webhook",
		map[string]any{"enabled": enabled, "includeUserEvents": includeUser, "includeAdminEvents": includeAdmin, "secretRotated": plaintextSecret != ""})
	response := map[string]any{"ok": true, "enabled": enabled, "hasSecret": sealed != ""}
	if plaintextSecret != "" {
		response["secret"] = plaintextSecret
		response["secretNotice"] = "Copy this signing secret now. It will not be shown again."
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) adminRotateSIEMSecret(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var exists bool
	_ = s.store.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM public.tenant_siem_config WHERE tenant_id=$1)`, sess.ActiveTenantID).Scan(&exists)
	if !exists {
		writeError(w, http.StatusConflict, "SIEM_NOT_CONFIGURED", "Configure the SIEM endpoint first")
		return
	}
	secret, err := newSIEMSecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SIEM_SECRET", "Could not generate a signing secret")
		return
	}
	sealed, err := s.sealSecuritySecret(secret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SIEM_SECRET", "Could not protect the signing secret")
		return
	}
	if _, err := s.store.DB.Exec(r.Context(), `UPDATE public.tenant_siem_config SET secret_sealed=$2, updated_at=now() WHERE tenant_id=$1`, sess.ActiveTenantID, sealed); err != nil {
		writeError(w, http.StatusInternalServerError, "SIEM", "Could not rotate the signing secret")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.siem_secret_rotated", "SIEM signing secret", "Rotated the SIEM signing secret", map[string]any{})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "secret": secret, "secretNotice": "Copy this signing secret now. It will not be shown again."})
}

func (s *Server) adminTestSIEM(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	config := s.loadSIEMConfig(r.Context(), sess.ActiveTenantID)
	if config.Endpoint == "" || config.SecretSealed == "" {
		writeError(w, http.StatusConflict, "SIEM_NOT_CONFIGURED", "Configure and save the SIEM endpoint before testing")
		return
	}
	secret, err := s.openSecuritySecret(config.SecretSealed)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SIEM_SECRET", "Could not open the signing secret")
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"eventId": "test_" + newID("evt"), "tenantId": sess.ActiveTenantID, "source": "test",
		"action": "siem.test", "actor": sess.User.Email, "target": "SIEM integration",
		"detail": "Alt8Mail test event from the organization console", "createdAt": time.Now().UTC().Format(time.RFC3339),
		"test": true,
	})
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	status, err := deliverSIEMEvent(ctx, config.Endpoint, secret, newID("dlv"), "test", payload)
	if err != nil {
		_, _ = s.store.DB.Exec(r.Context(), `UPDATE public.tenant_siem_config SET last_error=$2, updated_at=now() WHERE tenant_id=$1`, sess.ActiveTenantID, truncate(err.Error(), 500))
		s.recordAdminAudit(r.Context(), sess, sess.User, "admin.siem_test_failed", config.Endpoint, "SIEM test delivery failed", map[string]any{"error": truncate(err.Error(), 300)})
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "status": status, "error": truncate(err.Error(), 500)})
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `UPDATE public.tenant_siem_config SET last_delivery_at=now(), last_error='', updated_at=now() WHERE tenant_id=$1`, sess.ActiveTenantID)
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.siem_test_succeeded", config.Endpoint, "SIEM test delivery succeeded", map[string]any{})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": status})
}

func (s *Server) adminSIEMDeliveries(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, source, source_id, event_action, status, attempts, last_error, created_at, delivered_at
		FROM public.tenant_siem_deliveries WHERE tenant_id=$1 ORDER BY created_at DESC LIMIT 100
	`, sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SIEM", "Could not load SIEM deliveries")
		return
	}
	defer rows.Close()
	deliveries := []map[string]any{}
	for rows.Next() {
		var id, source, action, status, lastError string
		var sourceID int64
		var attempts int
		var createdAt time.Time
		var deliveredAt *time.Time
		if rows.Scan(&id, &source, &sourceID, &action, &status, &attempts, &lastError, &createdAt, &deliveredAt) != nil {
			continue
		}
		var delivered any
		if deliveredAt != nil {
			delivered = deliveredAt.UTC().Format(time.RFC3339)
		}
		deliveries = append(deliveries, map[string]any{
			"id": id, "source": source, "sourceId": sourceID, "action": action,
			"status": status, "attempts": attempts, "lastError": lastError,
			"createdAt": createdAt.UTC().Format(time.RFC3339), "deliveredAt": delivered,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": deliveries})
}

func (s *Server) siemWorker(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.dispatchSIEM(ctx)
		}
	}
}

func (s *Server) dispatchSIEM(ctx context.Context) {
	if !s.entitled(FeatureAuditExport) {
		return
	}
	s.collectSIEMEvents(ctx)
	s.deliverPendingSIEM(ctx)
}

func (s *Server) collectSIEMEvents(ctx context.Context) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT c.tenant_id::text, c.include_user_events, c.include_admin_events,
		       c.last_user_audit_id, c.last_admin_audit_id
		FROM public.tenant_siem_config c
		JOIN public.tenants t ON t.id=c.tenant_id
		WHERE c.enabled AND c.endpoint <> '' AND t.status='active'
	`)
	if err != nil {
		return
	}
	configs := []siemConfig{}
	for rows.Next() {
		var config siemConfig
		if rows.Scan(&config.TenantID, &config.IncludeUserEvents, &config.IncludeAdminEvents,
			&config.LastUserAuditID, &config.LastAdminAuditID) == nil {
			configs = append(configs, config)
		}
	}
	rows.Close()
	for _, config := range configs {
		if config.IncludeUserEvents {
			s.collectSIEMUserEvents(ctx, config)
		}
		if config.IncludeAdminEvents {
			s.collectSIEMAdminEvents(ctx, config)
		}
	}
}

func (s *Server) collectSIEMUserEvents(ctx context.Context, config siemConfig) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, actor, action, target, detail, COALESCE(ip::text,''), user_agent, created_at
		FROM public.audit_log WHERE tenant_id=$1 AND id > $2 ORDER BY id ASC LIMIT 100
	`, config.TenantID, config.LastUserAuditID)
	if err != nil {
		return
	}
	defer rows.Close()
	type event struct {
		id        int64
		action    string
		payload   []byte
		createdAt time.Time
	}
	events := []event{}
	for rows.Next() {
		var id int64
		var actor, action, target, detail, ip, userAgent string
		var createdAt time.Time
		if rows.Scan(&id, &actor, &action, &target, &detail, &ip, &userAgent, &createdAt) != nil {
			continue
		}
		payload, _ := json.Marshal(map[string]any{
			"eventId": fmt.Sprintf("user_%d", id), "tenantId": config.TenantID, "source": "user",
			"action": action, "actor": actor, "target": target, "detail": detail,
			"ip": ip, "userAgent": userAgent, "createdAt": createdAt.UTC().Format(time.RFC3339),
		})
		events = append(events, event{id: id, action: action, payload: payload, createdAt: createdAt})
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(events) == 0 {
		return
	}
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)
	maxID := config.LastUserAuditID
	for _, item := range events {
		if _, err := tx.Exec(ctx, `
			INSERT INTO public.tenant_siem_deliveries
			  (id, tenant_id, source, source_id, event_action, payload, status, next_attempt_at, created_at)
			VALUES ($1,$2,'user',$3,$4,$5,'pending',now(),$6)
			ON CONFLICT (tenant_id, source, source_id) DO NOTHING
		`, newID("siem"), config.TenantID, item.id, truncate(item.action, 120), item.payload, item.createdAt); err != nil {
			return
		}
		if item.id > maxID {
			maxID = item.id
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE public.tenant_siem_config SET last_user_audit_id=$2 WHERE tenant_id=$1`, config.TenantID, maxID); err != nil {
		return
	}
	_ = tx.Commit(ctx)
}

func (s *Server) collectSIEMAdminEvents(ctx context.Context, config siemConfig) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, actor_email::text, action, target, detail, COALESCE(ip::text,''), user_agent, metadata, created_at
		FROM public.admin_audit_log WHERE tenant_id=$1 AND id > $2 ORDER BY id ASC LIMIT 100
	`, config.TenantID, config.LastAdminAuditID)
	if err != nil {
		return
	}
	defer rows.Close()
	type event struct {
		id        int64
		action    string
		payload   []byte
		createdAt time.Time
	}
	events := []event{}
	for rows.Next() {
		var id int64
		var actor, action, target, detail, ip, userAgent string
		var metadata []byte
		var createdAt time.Time
		if rows.Scan(&id, &actor, &action, &target, &detail, &ip, &userAgent, &metadata, &createdAt) != nil {
			continue
		}
		decoded := map[string]any{}
		_ = json.Unmarshal(metadata, &decoded)
		payload, _ := json.Marshal(map[string]any{
			"eventId": fmt.Sprintf("admin_%d", id), "tenantId": config.TenantID, "source": "admin",
			"action": action, "actor": actor, "target": target, "detail": detail,
			"ip": ip, "userAgent": userAgent, "metadata": decoded,
			"createdAt": createdAt.UTC().Format(time.RFC3339),
		})
		events = append(events, event{id: id, action: action, payload: payload, createdAt: createdAt})
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(events) == 0 {
		return
	}
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)
	maxID := config.LastAdminAuditID
	for _, item := range events {
		if _, err := tx.Exec(ctx, `
			INSERT INTO public.tenant_siem_deliveries
			  (id, tenant_id, source, source_id, event_action, payload, status, next_attempt_at, created_at)
			VALUES ($1,$2,'admin',$3,$4,$5,'pending',now(),$6)
			ON CONFLICT (tenant_id, source, source_id) DO NOTHING
		`, newID("siem"), config.TenantID, item.id, truncate(item.action, 120), item.payload, item.createdAt); err != nil {
			return
		}
		if item.id > maxID {
			maxID = item.id
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE public.tenant_siem_config SET last_admin_audit_id=$2 WHERE tenant_id=$1`, config.TenantID, maxID); err != nil {
		return
	}
	_ = tx.Commit(ctx)
}

type siemPendingDelivery struct {
	ID           string
	TenantID     string
	Source       string
	Payload      []byte
	Attempts     int
	Endpoint     string
	SecretSealed string
}

func (s *Server) deliverPendingSIEM(ctx context.Context) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT d.id, d.tenant_id::text, d.source, d.payload, d.attempts, c.endpoint, c.secret_sealed
		FROM public.tenant_siem_deliveries d
		JOIN public.tenant_siem_config c ON c.tenant_id=d.tenant_id
		WHERE d.status IN ('pending','failed') AND d.next_attempt_at <= now()
		  AND c.enabled AND c.endpoint <> ''
		ORDER BY d.created_at ASC LIMIT 50
	`)
	if err != nil {
		return
	}
	deliveries := []siemPendingDelivery{}
	for rows.Next() {
		var item siemPendingDelivery
		if rows.Scan(&item.ID, &item.TenantID, &item.Source, &item.Payload, &item.Attempts, &item.Endpoint, &item.SecretSealed) == nil {
			deliveries = append(deliveries, item)
		}
	}
	rows.Close()
	for _, item := range deliveries {
		if ctx.Err() != nil {
			return
		}
		secret, err := s.openSecuritySecret(item.SecretSealed)
		if err != nil {
			s.failSIEMDelivery(ctx, item, 0, "Could not open the signing secret")
			continue
		}
		deliveryCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		status, err := deliverSIEMEvent(deliveryCtx, item.Endpoint, secret, item.ID, item.Source, item.Payload)
		cancel()
		if err != nil {
			s.failSIEMDelivery(ctx, item, status, err.Error())
			continue
		}
		_, _ = s.store.DB.Exec(ctx, `
			UPDATE public.tenant_siem_deliveries
			SET status='delivered', attempts=attempts+1, delivered_at=now(), last_error=''
			WHERE id=$1
		`, item.ID)
		_, _ = s.store.DB.Exec(ctx, `
			UPDATE public.tenant_siem_config SET last_delivery_at=now(), last_error='', updated_at=now()
			WHERE tenant_id=$1
		`, item.TenantID)
	}
}

func (s *Server) failSIEMDelivery(ctx context.Context, item siemPendingDelivery, status int, message string) {
	attempts := item.Attempts + 1
	backoff := 24 * time.Hour
	switch {
	case attempts <= 1:
		backoff = time.Minute
	case attempts == 2:
		backoff = 5 * time.Minute
	case attempts == 3:
		backoff = 15 * time.Minute
	case attempts == 4:
		backoff = time.Hour
	case attempts <= 6:
		backoff = 6 * time.Hour
	}
	safeMessage := truncate(sanitizeMailText(message), 500)
	_, _ = s.store.DB.Exec(ctx, `
		UPDATE public.tenant_siem_deliveries
		SET status='failed', attempts=$2, last_error=$3, next_attempt_at=now()+$4::interval
		WHERE id=$1
	`, item.ID, attempts, safeMessage, fmt.Sprintf("%d seconds", int(backoff.Seconds())))
	_, _ = s.store.DB.Exec(ctx, `
		UPDATE public.tenant_siem_config SET last_error=$2, updated_at=now() WHERE tenant_id=$1
	`, item.TenantID, safeMessage)
}
