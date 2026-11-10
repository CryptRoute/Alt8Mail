package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var campaignHrefPattern = regexp.MustCompile(`href="(https?://[^"]+)"`)

var campaignPixel = []byte{
	0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00, 0x01, 0x00, 0x80, 0x00, 0x00,
	0x00, 0x00, 0x00, 0xff, 0xff, 0xff, 0x21, 0xf9, 0x04, 0x01, 0x00, 0x00, 0x00,
	0x00, 0x2c, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x02, 0x02,
	0x44, 0x01, 0x00, 0x3b,
}

func (s *Server) addCampaignTracking(ctx context.Context, user userRecord, mergeID, outboxID, email, bodyHTML, baseURL string) (string, string, error) {
	if strings.TrimSpace(bodyHTML) == "" {
		return bodyHTML, "", nil
	}
	trackingID := newID("tracking")
	token := newID("open")
	if _, err := s.store.DB.Exec(ctx, `
		INSERT INTO public.campaign_tracking (id, tenant_id, user_id, merge_id, recipient_email, token)
		VALUES ($1,$2,$3,NULLIF($4,''),$5,$6)
	`, trackingID, user.TenantID, user.ID, mergeID, email, token); err != nil {
		return bodyHTML, "", err
	}
	rendered := campaignHrefPattern.ReplaceAllStringFunc(bodyHTML, func(match string) string {
		parts := campaignHrefPattern.FindStringSubmatch(match)
		if len(parts) != 2 {
			return match
		}
		target, err := url.Parse(parts[1])
		if err != nil || (target.Scheme != "http" && target.Scheme != "https") {
			return match
		}
		linkToken := newID("click")
		if _, err := s.store.DB.Exec(ctx, `
			INSERT INTO public.campaign_links (token, tracking_id, url) VALUES ($1,$2,$3)
		`, linkToken, trackingID, target.String()); err != nil {
			return match
		}
		return `href="` + baseURL + `/t/c/` + linkToken + `"`
	})
	pixel := `<img src="` + baseURL + `/t/o/` + token + `.gif" width="1" height="1" alt="" style="display:none">`
	return rendered + pixel, trackingID, nil
}

func (s *Server) recordCampaignOpen(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.PathValue("token"))
	var trackingID string
	if err := s.store.DB.QueryRow(r.Context(), `SELECT id FROM public.campaign_tracking WHERE token=$1`, token).Scan(&trackingID); err != nil {
		http.NotFound(w, r)
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `UPDATE public.campaign_tracking SET open_count=open_count+1, last_event_at=now() WHERE id=$1`, trackingID)
	_, _ = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.campaign_events (tracking_id, kind, ip_hash, user_agent) VALUES ($1,'open',$2,$3)
	`, trackingID, campaignIPHash(clientIP(r)), sanitizeUserAgent(r.UserAgent()))
	w.Header().Set("Content-Type", "image/gif")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(campaignPixel)
}

func (s *Server) recordCampaignClick(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.PathValue("token"))
	var trackingID, rawURL string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT c.tracking_id, c.url FROM public.campaign_links c WHERE c.token=$1
	`, token).Scan(&trackingID, &rawURL); err != nil {
		http.NotFound(w, r)
		return
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		http.NotFound(w, r)
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `UPDATE public.campaign_tracking SET click_count=click_count+1, last_event_at=now() WHERE id=$1`, trackingID)
	_, _ = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.campaign_events (tracking_id, kind, url, ip_hash, user_agent) VALUES ($1,'click',$2,$3,$4)
	`, trackingID, parsed.String(), campaignIPHash(clientIP(r)), sanitizeUserAgent(r.UserAgent()))
	http.Redirect(w, r, parsed.String(), http.StatusTemporaryRedirect)
}

func campaignIPHash(ip string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(ip)))
	return hex.EncodeToString(sum[:])
}

func (s *Server) campaignTrackingReport(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	mergeID := strings.TrimSpace(r.URL.Query().Get("mergeId"))
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT recipient_email::text, open_count, click_count, created_at, last_event_at
		FROM public.campaign_tracking WHERE user_id=$1 AND ($2 = '' OR merge_id=$2)
		ORDER BY created_at DESC LIMIT 500
	`, sess.User.ID, mergeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TRACKING", "Could not load campaign tracking")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	var opens, clicks int
	for rows.Next() {
		var email string
		var openCount, clickCount int
		var created time.Time
		var last *time.Time
		if rows.Scan(&email, &openCount, &clickCount, &created, &last) == nil {
			opens += openCount
			clicks += clickCount
			item := map[string]any{"recipientEmail": email, "opens": openCount, "clicks": clickCount, "createdAt": created.UTC().Format(time.RFC3339)}
			if last != nil {
				item["lastEventAt"] = last.UTC().Format(time.RFC3339)
			}
			items = append(items, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tracking": items, "opens": opens, "clicks": clicks})
}

func (s *Server) registerCampaignTrackingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /t/o/{token}.gif", s.recordCampaignOpen)
	mux.HandleFunc("GET /t/c/{token}", s.recordCampaignClick)
	mux.HandleFunc("GET /api/campaign-tracking", s.requireUser(s.campaignTrackingReport))
}
