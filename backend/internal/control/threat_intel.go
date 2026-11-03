package control

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/threatintel"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const threatShareSetting = "security.rspamd_share"

type threatIndicatorSource interface {
	ConfirmedIndicators(ctx context.Context) ([]threatintel.ReportIndicators, error)
}

type postgresThreatSource struct {
	db *pgxpool.Pool
}

func (p postgresThreatSource) ConfirmedIndicators(ctx context.Context) ([]threatintel.ReportIndicators, error) {
	rows, err := p.db.Query(ctx, `
		SELECT lower(r.sender_email::text), lower(r.sender_domain),
		       COALESCE(array_agg(h.host) FILTER (WHERE h.approved), ARRAY[]::text[])
		FROM public.security_reports r
		LEFT JOIN public.security_report_url_hosts h ON h.report_id = r.id
		WHERE r.status = 'confirmed'
		GROUP BY r.id, r.sender_email, r.sender_domain
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	reports := []threatintel.ReportIndicators{}
	for rows.Next() {
		var report threatintel.ReportIndicators
		if err := rows.Scan(&report.Sender, &report.Domain, &report.URLHosts); err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	return reports, rows.Err()
}

func syncThreatIndicators(ctx context.Context, source threatIndicatorSource, enabled bool, client redis.UniversalClient) error {
	if !enabled {
		// The setting is off: never touch Redis as part of a normal rebuild.
		return nil
	}
	reports, err := source.ConfirmedIndicators(ctx)
	if err != nil {
		return fmt.Errorf("load confirmed indicators: %w", err)
	}
	return threatintel.Publish(ctx, client, threatintel.ActiveSet(reports))
}

func (s *Server) threatRedisClient(ctx context.Context) (*redis.Client, error) {
	rawPassword, err := os.ReadFile(s.cfg.RedisPasswordFile)
	if err != nil {
		return nil, fmt.Errorf("read the Redis password: %w", err)
	}
	address := strings.TrimSpace(s.cfg.RedisAddr)
	if address == "" {
		address = "127.0.0.1:6379"
	}
	client := redis.NewClient(&redis.Options{
		Addr:         address,
		Password:     strings.TrimSpace(string(rawPassword)),
		DB:           0,
		DialTimeout:  3 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	})
	pingCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("connect to Redis at %s: %w", address, err)
	}
	return client, nil
}

// refreshThreatIndicatorsLocked must be called with threatMu held.
func (s *Server) refreshThreatIndicatorsLocked(ctx context.Context) error {
	if !s.settingBool(ctx, threatShareSetting, false) {
		return nil
	}
	client, err := s.threatRedisClient(ctx)
	if err != nil {
		return err
	}
	defer client.Close()
	return syncThreatIndicators(ctx, postgresThreatSource{db: s.db}, true, client)
}

func (s *Server) clearThreatIndicatorsLocked(ctx context.Context) error {
	client, err := s.threatRedisClient(ctx)
	if err != nil {
		return err
	}
	defer client.Close()
	return threatintel.Clear(ctx, client)
}

func (s *Server) threatIndicatorCounts(ctx context.Context) (map[string]int, error) {
	reports, err := (postgresThreatSource{db: s.db}).ConfirmedIndicators(ctx)
	if err != nil {
		return nil, err
	}
	set := threatintel.ActiveSet(reports)
	return map[string]int{
		"senders": len(set.Senders),
		"domains": len(set.Domains),
		"hosts":   len(set.URLs),
	}, nil
}

func (s *Server) registerThreatIntelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/security/threat-intel", s.require("security.read", s.handleThreatIntelGet))
	mux.HandleFunc("PUT /control/api/security/threat-intel", s.require("security.manage", s.handleThreatIntelSave))
}

func (s *Server) handleThreatIntelGet(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	counts, err := s.threatIndicatorCounts(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": s.settingBool(r.Context(), threatShareSetting, false),
		"active":  counts,
	})
}

func (s *Server) handleThreatIntelSave(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSON(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if body.Enabled == nil {
		writeError(w, http.StatusBadRequest, "ENABLED", "enabled is required")
		return
	}
	enabled := *body.Enabled

	s.threatMu.Lock()
	defer s.threatMu.Unlock()
	current := s.settingBool(r.Context(), threatShareSetting, false)
	if enabled {
		if err := s.setSetting(r.Context(), threatShareSetting, true, sess.User.ID); err != nil {
			writeFromError(w, err)
			return
		}
		if err := s.refreshThreatIndicatorsLocked(r.Context()); err != nil {
			// Do not leave the setting enabled when the maps could not be
			// populated. Clear best-effort and restore the previous state.
			_ = s.clearThreatIndicatorsLocked(r.Context())
			_ = s.setSetting(r.Context(), threatShareSetting, false, sess.User.ID)
			writeError(w, http.StatusBadGateway, "THREAT_SYNC", "Could not publish threat indicators to Redis: "+err.Error())
			return
		}
	} else if current {
		// Clear first: if Redis is unavailable, keep the setting enabled so the
		// operator does not believe sharing is off while stale maps remain.
		if err := s.clearThreatIndicatorsLocked(r.Context()); err != nil {
			writeError(w, http.StatusBadGateway, "THREAT_SYNC", "Could not clear threat indicators from Redis: "+err.Error())
			return
		}
		if err := s.setSetting(r.Context(), threatShareSetting, false, sess.User.ID); err != nil {
			writeFromError(w, err)
			return
		}
	}
	counts, _ := s.threatIndicatorCounts(r.Context())
	s.actorAudit(r, sess, "security.threat_intelligence_updated", "Threat intelligence",
		"Updated Rspamd threat indicator sharing", map[string]any{"enabled": enabled})
	s.notifyEvent(r.Context(), "security.threat_intelligence_updated", map[string]any{"enabled": enabled})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": enabled, "active": counts})
}

func (s *Server) reconcileThreatIndicators(ctx context.Context) {
	s.threatMu.Lock()
	defer s.threatMu.Unlock()
	if !s.settingBool(ctx, threatShareSetting, false) {
		return
	}
	if err := s.refreshThreatIndicatorsLocked(ctx); err != nil {
		s.logger.Warn("threat indicator reconciliation failed", "err", err)
	}
}

func (s *Server) runThreatIndicatorReconciler(ctx context.Context) {
	reconcile := func() { s.reconcileThreatIndicators(ctx) }
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	reconcile()
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile()
		}
	}
}
