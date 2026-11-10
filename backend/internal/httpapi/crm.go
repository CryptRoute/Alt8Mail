package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type hubspotObject struct {
	ID         string         `json:"id"`
	Properties map[string]any `json:"properties"`
}

type hubspotPage struct {
	Results []hubspotObject `json:"results"`
	Paging  struct {
		Next struct {
			After string `json:"after"`
		} `json:"next"`
	} `json:"paging"`
}

func (s *Server) hubspotBase() string {
	if strings.TrimSpace(s.hubspotBaseURL) != "" {
		return strings.TrimRight(strings.TrimSpace(s.hubspotBaseURL), "/")
	}
	return "https://api.hubapi.com"
}

func (s *Server) hubspotFetch(ctx context.Context, token, path string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.hubspotBase()+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Alt8Mail-CRM/1.0")
	client := &http.Client{Timeout: 20 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("HubSpot returned HTTP %d", response.StatusCode)
	}
	return json.Unmarshal(raw, target)
}

func (s *Server) syncHubSpot(ctx context.Context, tenantID string) (int, error) {
	connection, ok := s.addonActiveConnection(ctx, tenantID, "hubspot", "", "tenant")
	if !ok {
		return 0, fmt.Errorf("connect the HubSpot add-on before syncing CRM records")
	}
	plain, err := s.openWorkspaceToken(connection.Sealed)
	if err != nil {
		return 0, fmt.Errorf("the HubSpot credential is unavailable")
	}
	var credentials map[string]string
	if err := json.Unmarshal([]byte(plain), &credentials); err != nil || strings.TrimSpace(credentials["accessToken"]) == "" {
		return 0, fmt.Errorf("the HubSpot access token is unavailable")
	}
	token := credentials["accessToken"]
	synced := 0
	upsert := func(objectType, externalID, email, name, company, title, stage string, amount float64, raw any) error {
		rawJSON, _ := json.Marshal(raw)
		_, err := s.store.DB.Exec(ctx, `
			INSERT INTO public.crm_records
			  (id, tenant_id, provider, object_type, external_id, email, name, company, title, stage, amount, raw, last_synced_at)
			VALUES ($1,$2,'hubspot',$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,now())
			ON CONFLICT (tenant_id, provider, object_type, external_id) DO UPDATE SET
			  email=EXCLUDED.email, name=EXCLUDED.name, company=EXCLUDED.company,
			  title=EXCLUDED.title, stage=EXCLUDED.stage, amount=EXCLUDED.amount,
			  raw=EXCLUDED.raw, last_synced_at=now()
		`, newID("crm"), tenantID, objectType, externalID, email, name, company, title, stage, amount, rawJSON)
		if err == nil {
			synced++
		}
		return err
	}
	after := ""
	for pages := 0; pages < 20; pages++ {
		path := "/crm/v3/objects/contacts?limit=100&properties=email,firstname,lastname,company,jobtitle,lifecyclestage"
		if after != "" {
			path += "&after=" + url.QueryEscape(after)
		}
		var page hubspotPage
		if err := s.hubspotFetch(ctx, token, path, &page); err != nil {
			return synced, err
		}
		for _, contact := range page.Results {
			props := contact.Properties
			email := strings.ToLower(strings.TrimSpace(fmt.Sprint(props["email"])))
			name := strings.TrimSpace(fmt.Sprint(props["firstname"]) + " " + fmt.Sprint(props["lastname"]))
			if name == "" || name == "<nil> <nil>" {
				name = email
			}
			_ = upsert("contact", contact.ID, email, name, fmt.Sprint(props["company"]), fmt.Sprint(props["jobtitle"]), fmt.Sprint(props["lifecyclestage"]), 0, contact)
		}
		after = page.Paging.Next.After
		if after == "" {
			break
		}
	}
	after = ""
	for pages := 0; pages < 20; pages++ {
		path := "/crm/v3/objects/deals?limit=100&properties=dealname,amount,dealstage,closedate"
		if after != "" {
			path += "&after=" + url.QueryEscape(after)
		}
		var page hubspotPage
		if err := s.hubspotFetch(ctx, token, path, &page); err != nil {
			return synced, err
		}
		for _, deal := range page.Results {
			props := deal.Properties
			amount := 0.0
			fmt.Sscanf(fmt.Sprint(props["amount"]), "%f", &amount)
			_ = upsert("deal", deal.ID, "", fmt.Sprint(props["dealname"]), "", "", fmt.Sprint(props["dealstage"]), amount, deal)
		}
		after = page.Paging.Next.After
		if after == "" {
			break
		}
	}
	return synced, nil
}

func (s *Server) adminSyncCRM(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	provider, ok := normalizeCRMProvider(r.URL.Query().Get("provider"))
	if !ok {
		writeError(w, http.StatusNotImplemented, "CRM_PROVIDER", "Choose a supported CRM provider")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.crm_sync_state (tenant_id, provider, status) VALUES ($1,$2,'running')
		ON CONFLICT (tenant_id, provider) DO UPDATE SET status='running', last_error=''
	`, sess.ActiveTenantID, provider)
	count, err := s.syncCRM(r.Context(), sess.ActiveTenantID, provider)
	if err != nil {
		_, _ = s.store.DB.Exec(r.Context(), `
			UPDATE public.crm_sync_state SET status='failed', last_error=$3, last_sync_at=now()
			WHERE tenant_id=$1 AND provider=$2
		`, sess.ActiveTenantID, provider, truncate(err.Error(), 500))
		writeError(w, http.StatusBadGateway, "CRM_SYNC", err.Error())
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `
		UPDATE public.crm_sync_state SET status='success', records=$3, last_sync_at=now(), last_error=''
		WHERE tenant_id=$1 AND provider=$2
	`, sess.ActiveTenantID, provider, count)
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.crm_synced", provider, "Synced CRM records", map[string]any{"records": count})
	s.enqueueWebhookEvent(r.Context(), sess.ActiveTenantID, "crm.synced", map[string]any{
		"provider": provider, "records": count, "tenantId": sess.ActiveTenantID,
		"syncedAt": time.Now().UTC().Format(time.RFC3339),
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "provider": provider, "records": count})
}

func (s *Server) adminCRMRecords(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	email := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("email")))
	provider := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("provider")))
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, provider, object_type, external_id, email::text, name, company, title, stage, amount, last_synced_at
		FROM public.crm_records
		WHERE tenant_id=$1 AND ($2='' OR email=$2) AND ($3='' OR provider=$3)
		ORDER BY last_synced_at DESC LIMIT 500
	`, sess.ActiveTenantID, email, provider)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CRM", "Could not load CRM records")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, providerValue, objectType, externalID, emailValue, name, company, title, stage string
		var amount float64
		var synced time.Time
		if rows.Scan(&id, &providerValue, &objectType, &externalID, &emailValue, &name, &company, &title, &stage, &amount, &synced) == nil {
			items = append(items, map[string]any{
				"id": id, "provider": providerValue, "objectType": objectType, "externalId": externalID,
				"email": emailValue, "name": name, "company": company, "title": title, "stage": stage,
				"amount": amount, "lastSyncedAt": synced.UTC().Format(time.RFC3339),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": items, "count": len(items)})
}

func (s *Server) adminCRMState(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT provider, status, records, last_sync_at, last_error
		FROM public.crm_sync_state WHERE tenant_id=$1 ORDER BY provider
	`, sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CRM", "Could not load CRM sync state")
		return
	}
	defer rows.Close()
	states := []map[string]any{}
	for rows.Next() {
		var provider, status, lastError string
		var records int
		var lastSync *time.Time
		if rows.Scan(&provider, &status, &records, &lastSync, &lastError) == nil {
			item := map[string]any{"provider": provider, "status": status, "records": records, "lastError": lastError}
			if lastSync != nil {
				item["lastSyncAt"] = lastSync.UTC().Format(time.RFC3339)
			}
			states = append(states, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"states": states, "providers": supportedCRMProviders})
}
