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

// crmRecordInput is the provider-neutral shape every CRM adapter writes into
// the local CRM cache. Provider adapters only translate their API responses.
type crmRecordInput struct {
	ObjectType string
	ExternalID string
	Email      string
	Name       string
	Company    string
	Title      string
	Stage      string
	Amount     float64
	Raw        any
}

var supportedCRMProviders = []string{"hubspot", "salesforce", "zoho", "pipedrive"}

func normalizeCRMProvider(value string) (string, bool) {
	provider := strings.ToLower(strings.TrimSpace(value))
	if provider == "" {
		provider = "hubspot"
	}
	for _, supported := range supportedCRMProviders {
		if provider == supported {
			return provider, true
		}
	}
	return provider, false
}

func (s *Server) crmClient() *http.Client {
	if s.crmHTTPClient != nil {
		return s.crmHTTPClient
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (s *Server) crmCredentials(ctx context.Context, tenantID, provider string) (map[string]string, error) {
	connection, ok := s.addonActiveConnection(ctx, tenantID, provider, "", "tenant")
	if !ok {
		return nil, fmt.Errorf("connect the %s add-on before syncing CRM records", provider)
	}
	plain, err := s.openWorkspaceToken(connection.Sealed)
	if err != nil {
		return nil, fmt.Errorf("the %s credential is unavailable", provider)
	}
	var credentials map[string]string
	if err := json.Unmarshal([]byte(plain), &credentials); err != nil {
		return nil, fmt.Errorf("the %s credential is unreadable", provider)
	}
	return credentials, nil
}

func (s *Server) crmRequestJSON(ctx context.Context, method, endpoint string, form url.Values, headers map[string]string, target any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Alt8Mail-CRM/1.0")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := s.crmClient().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("the CRM provider returned HTTP %d", response.StatusCode)
	}
	if target == nil {
		return nil
	}
	return json.Unmarshal(raw, target)
}

func (s *Server) upsertCRMRecord(ctx context.Context, tenantID, provider string, record crmRecordInput) error {
	rawJSON, _ := json.Marshal(record.Raw)
	_, err := s.store.DB.Exec(ctx, `
		INSERT INTO public.crm_records
		  (id, tenant_id, provider, object_type, external_id, email, name, company, title, stage, amount, raw, last_synced_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb,now())
		ON CONFLICT (tenant_id, provider, object_type, external_id) DO UPDATE SET
		  email=EXCLUDED.email, name=EXCLUDED.name, company=EXCLUDED.company,
		  title=EXCLUDED.title, stage=EXCLUDED.stage, amount=EXCLUDED.amount,
		  raw=EXCLUDED.raw, last_synced_at=now()
	`, newID("crm"), tenantID, provider, record.ObjectType, record.ExternalID,
		strings.ToLower(strings.TrimSpace(record.Email)), record.Name, record.Company,
		record.Title, record.Stage, record.Amount, rawJSON)
	return err
}

func (s *Server) syncCRM(ctx context.Context, tenantID, provider string) (int, error) {
	provider, ok := normalizeCRMProvider(provider)
	if !ok {
		return 0, fmt.Errorf("this CRM sync adapter is not enabled")
	}
	switch provider {
	case "hubspot":
		return s.syncHubSpot(ctx, tenantID)
	case "salesforce":
		return s.syncSalesforce(ctx, tenantID)
	case "zoho":
		return s.syncZoho(ctx, tenantID)
	case "pipedrive":
		return s.syncPipedrive(ctx, tenantID)
	}
	return 0, fmt.Errorf("this CRM sync adapter is not enabled")
}

/* ------------------------------------------------------------
   Pipedrive
   ------------------------------------------------------------ */

type pipedriveEmail struct {
	Value   string `json:"value"`
	Primary bool   `json:"primary"`
}

func pipedrivePrimaryEmail(values []pipedriveEmail) string {
	for _, value := range values {
		if value.Primary && strings.TrimSpace(value.Value) != "" {
			return value.Value
		}
	}
	for _, value := range values {
		if strings.TrimSpace(value.Value) != "" {
			return value.Value
		}
	}
	return ""
}

type pipedrivePageInfo struct {
	MoreItems bool `json:"more_items"`
	NextStart int  `json:"next_start"`
}

func (s *Server) pipedriveBase() string {
	if value := strings.TrimSpace(s.pipedriveBaseURL); value != "" {
		return strings.TrimRight(value, "/")
	}
	return "https://api.pipedrive.com"
}

func (s *Server) syncPipedrive(ctx context.Context, tenantID string) (int, error) {
	credentials, err := s.crmCredentials(ctx, tenantID, "pipedrive")
	if err != nil {
		return 0, err
	}
	token := strings.TrimSpace(credentials["apiToken"])
	if token == "" {
		return 0, fmt.Errorf("the Pipedrive API token is unavailable")
	}
	headers := map[string]string{"x-api-token": token}
	synced := 0

	type personPage struct {
		Success bool `json:"success"`
		Data    []struct {
			ID      int              `json:"id"`
			Name    string           `json:"name"`
			OrgName string           `json:"org_name"`
			Email   []pipedriveEmail `json:"email"`
		} `json:"data"`
		AdditionalData struct {
			Pagination pipedrivePageInfo `json:"pagination"`
		} `json:"additional_data"`
	}
	start := 0
	for pages := 0; pages < 20; pages++ {
		var page personPage
		endpoint := fmt.Sprintf("%s/v1/persons?limit=100&start=%d", s.pipedriveBase(), start)
		if err := s.crmRequestJSON(ctx, http.MethodGet, endpoint, nil, headers, &page); err != nil {
			return synced, err
		}
		if !page.Success {
			return synced, fmt.Errorf("Pipedrive rejected the contact request")
		}
		for _, person := range page.Data {
			record := crmRecordInput{
				ObjectType: "contact", ExternalID: fmt.Sprint(person.ID),
				Email: pipedrivePrimaryEmail(person.Email), Name: person.Name,
				Company: person.OrgName, Raw: person,
			}
			if err := s.upsertCRMRecord(ctx, tenantID, "pipedrive", record); err == nil {
				synced++
			}
		}
		if !page.AdditionalData.Pagination.MoreItems || page.AdditionalData.Pagination.NextStart == 0 {
			break
		}
		start = page.AdditionalData.Pagination.NextStart
	}

	type dealPage struct {
		Success bool `json:"success"`
		Data    []struct {
			ID       int             `json:"id"`
			Title    string          `json:"title"`
			Value    float64         `json:"value"`
			StageID  int             `json:"stage_id"`
			PersonID json.RawMessage `json:"person_id"`
		} `json:"data"`
		AdditionalData struct {
			Pagination pipedrivePageInfo `json:"pagination"`
		} `json:"additional_data"`
	}
	start = 0
	for pages := 0; pages < 20; pages++ {
		var page dealPage
		endpoint := fmt.Sprintf("%s/v1/deals?limit=100&start=%d", s.pipedriveBase(), start)
		if err := s.crmRequestJSON(ctx, http.MethodGet, endpoint, nil, headers, &page); err != nil {
			return synced, err
		}
		if !page.Success {
			return synced, fmt.Errorf("Pipedrive rejected the deal request")
		}
		for _, deal := range page.Data {
			record := crmRecordInput{
				ObjectType: "deal", ExternalID: fmt.Sprint(deal.ID), Name: deal.Title,
				Email: pipedriveDealEmail(deal.PersonID), Stage: fmt.Sprint(deal.StageID),
				Amount: deal.Value, Raw: deal,
			}
			if err := s.upsertCRMRecord(ctx, tenantID, "pipedrive", record); err == nil {
				synced++
			}
		}
		if !page.AdditionalData.Pagination.MoreItems || page.AdditionalData.Pagination.NextStart == 0 {
			break
		}
		start = page.AdditionalData.Pagination.NextStart
	}
	return synced, nil
}

// pipedriveDealEmail accepts the two shapes Pipedrive uses for person_id: a
// numeric id on older endpoints, or an embedded person object.
func pipedriveDealEmail(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var person struct {
		Email []pipedriveEmail `json:"email"`
	}
	if err := json.Unmarshal(raw, &person); err == nil && len(person.Email) > 0 {
		return pipedrivePrimaryEmail(person.Email)
	}
	return ""
}

/* ------------------------------------------------------------
   Zoho CRM
   ------------------------------------------------------------ */

func zohoRegion(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "com":
		return "com", true
	case "eu", "in", "com.au", "jp":
		return strings.ToLower(strings.TrimSpace(value)), true
	}
	return "", false
}

func crmValueString(values map[string]any, key string) string {
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		if typed == float64(int64(typed)) {
			return fmt.Sprintf("%d", int64(typed))
		}
		return fmt.Sprintf("%v", typed)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case map[string]any:
		if name, ok := typed["name"].(string); ok {
			return name
		}
	}
	return fmt.Sprint(value)
}

func crmValueFloat(values map[string]any, key string) float64 {
	value, ok := values[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case float64:
		return typed
	case string:
		var parsed float64
		_, _ = fmt.Sscanf(typed, "%f", &parsed)
		return parsed
	case map[string]any:
		return crmValueFloat(typed, "value")
	}
	return 0
}

func (s *Server) syncZoho(ctx context.Context, tenantID string) (int, error) {
	credentials, err := s.crmCredentials(ctx, tenantID, "zoho")
	if err != nil {
		return 0, err
	}
	region, ok := zohoRegion(credentials["region"])
	if !ok {
		return 0, fmt.Errorf("the Zoho data centre is invalid")
	}
	clientID := strings.TrimSpace(credentials["clientId"])
	clientSecret := strings.TrimSpace(credentials["clientSecret"])
	refreshToken := strings.TrimSpace(credentials["refreshToken"])
	if clientID == "" || clientSecret == "" || refreshToken == "" {
		return 0, fmt.Errorf("the Zoho OAuth credentials are incomplete")
	}
	accountsBase := strings.TrimSpace(s.zohoAccountsBaseURL)
	if accountsBase == "" {
		accountsBase = "https://accounts.zoho." + region
	}
	apiBase := strings.TrimSpace(s.zohoCRMBaseURL)
	if apiBase == "" {
		apiBase = "https://www.zohoapis." + region
	}
	var token struct {
		AccessToken string `json:"access_token"`
		APIDomain   string `json:"api_domain"`
		Error       string `json:"error"`
	}
	err = s.crmRequestJSON(ctx, http.MethodPost, strings.TrimRight(accountsBase, "/")+"/oauth/v2/token", url.Values{
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"grant_type":    {"refresh_token"},
	}, nil, &token)
	if err != nil {
		return 0, err
	}
	if token.Error != "" || token.AccessToken == "" {
		return 0, fmt.Errorf("Zoho rejected the refresh token")
	}
	if s.zohoCRMBaseURL == "" && strings.TrimSpace(token.APIDomain) != "" {
		apiBase = token.APIDomain
	}
	headers := map[string]string{"Authorization": "Zoho-oauthtoken " + token.AccessToken}
	synced := 0

	type zohoPage struct {
		Data []map[string]any `json:"data"`
		Info struct {
			MoreRecords bool `json:"more_records"`
		} `json:"info"`
	}
	fetch := func(module, fields string, handle func(map[string]any)) error {
		for page := 1; page <= 20; page++ {
			var payload zohoPage
			endpoint := fmt.Sprintf("%s/crm/v5/%s?fields=%s&per_page=100&page=%d", strings.TrimRight(apiBase, "/"), module, fields, page)
			if err := s.crmRequestJSON(ctx, http.MethodGet, endpoint, nil, headers, &payload); err != nil {
				return err
			}
			for _, row := range payload.Data {
				handle(row)
			}
			if !payload.Info.MoreRecords {
				break
			}
		}
		return nil
	}
	if err := fetch("Contacts", "Email,Full_Name,Company,Title,Lead_Status", func(row map[string]any) {
		record := crmRecordInput{
			ObjectType: "contact", ExternalID: crmValueString(row, "id"),
			Email: crmValueString(row, "Email"), Name: crmValueString(row, "Full_Name"),
			Company: crmValueString(row, "Company"), Title: crmValueString(row, "Title"),
			Stage: crmValueString(row, "Lead_Status"), Raw: row,
		}
		if record.ExternalID == "" {
			return
		}
		if err := s.upsertCRMRecord(ctx, tenantID, "zoho", record); err == nil {
			synced++
		}
	}); err != nil {
		return synced, err
	}
	if err := fetch("Deals", "Deal_Name,Amount,Stage,Contact_Name", func(row map[string]any) {
		record := crmRecordInput{
			ObjectType: "deal", ExternalID: crmValueString(row, "id"),
			Name: crmValueString(row, "Deal_Name"), Stage: crmValueString(row, "Stage"),
			Amount: crmValueFloat(row, "Amount"), Raw: row,
		}
		if record.ExternalID == "" {
			return
		}
		if err := s.upsertCRMRecord(ctx, tenantID, "zoho", record); err == nil {
			synced++
		}
	}); err != nil {
		return synced, err
	}
	return synced, nil
}

/* ------------------------------------------------------------
   Salesforce
   ------------------------------------------------------------ */

func salesforceLoginAllowed(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" {
		return false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "login.salesforce.com", "test.salesforce.com":
		return parsed.Path == "" || parsed.Path == "/"
	}
	return false
}

func (s *Server) syncSalesforce(ctx context.Context, tenantID string) (int, error) {
	credentials, err := s.crmCredentials(ctx, tenantID, "salesforce")
	if err != nil {
		return 0, err
	}
	loginURL := strings.TrimRight(strings.TrimSpace(credentials["loginUrl"]), "/")
	if loginURL == "" {
		loginURL = "https://login.salesforce.com"
	}
	if s.salesforceBaseURL != "" {
		loginURL = strings.TrimRight(s.salesforceBaseURL, "/")
	} else if !salesforceLoginAllowed(loginURL) {
		return 0, fmt.Errorf("the Salesforce login URL is invalid")
	}
	clientID := strings.TrimSpace(credentials["clientId"])
	clientSecret := strings.TrimSpace(credentials["clientSecret"])
	refreshToken := strings.TrimSpace(credentials["refreshToken"])
	if clientID == "" || clientSecret == "" || refreshToken == "" {
		return 0, fmt.Errorf("the Salesforce OAuth credentials are incomplete")
	}
	var token struct {
		AccessToken      string `json:"access_token"`
		InstanceURL      string `json:"instance_url"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	err = s.crmRequestJSON(ctx, http.MethodPost, loginURL+"/services/oauth2/token", url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"refresh_token": {refreshToken},
	}, nil, &token)
	if err != nil {
		return 0, err
	}
	if token.AccessToken == "" || strings.TrimSpace(token.InstanceURL) == "" || token.Error != "" {
		return 0, fmt.Errorf("Salesforce rejected the refresh token")
	}
	instanceURL := strings.TrimRight(token.InstanceURL, "/")
	if s.salesforceBaseURL != "" {
		instanceURL = strings.TrimRight(s.salesforceBaseURL, "/")
	} else {
		instance, err := url.Parse(instanceURL)
		if err != nil || instance.Scheme != "https" {
			return 0, fmt.Errorf("Salesforce returned an invalid instance URL")
		}
	}
	headers := map[string]string{"Authorization": "Bearer " + token.AccessToken}
	synced := 0
	query := func(soql string, handle func(map[string]any)) error {
		next := "/services/data/v60.0/query?q=" + url.QueryEscape(soql)
		for pages := 0; pages < 20 && next != ""; pages++ {
			var payload struct {
				Records        []map[string]any `json:"records"`
				Done           bool             `json:"done"`
				NextRecordsURL string           `json:"nextRecordsUrl"`
			}
			if err := s.crmRequestJSON(ctx, http.MethodGet, instanceURL+next, nil, headers, &payload); err != nil {
				return err
			}
			for _, row := range payload.Records {
				handle(row)
			}
			if payload.Done {
				break
			}
			next = payload.NextRecordsURL
		}
		return nil
	}
	if err := query("SELECT Id, Email, Name, Title, LeadSource, Account.Name FROM Contact", func(row map[string]any) {
		record := crmRecordInput{
			ObjectType: "contact", ExternalID: crmValueString(row, "Id"),
			Email: crmValueString(row, "Email"), Name: crmValueString(row, "Name"),
			Title: crmValueString(row, "Title"), Stage: crmValueString(row, "LeadSource"),
			Raw: row,
		}
		if account, ok := row["Account"].(map[string]any); ok {
			record.Company = crmValueString(account, "Name")
		}
		if record.ExternalID == "" {
			return
		}
		if err := s.upsertCRMRecord(ctx, tenantID, "salesforce", record); err == nil {
			synced++
		}
	}); err != nil {
		return synced, err
	}
	if err := query("SELECT Id, Name, Amount, StageName FROM Opportunity", func(row map[string]any) {
		record := crmRecordInput{
			ObjectType: "deal", ExternalID: crmValueString(row, "Id"),
			Name: crmValueString(row, "Name"), Stage: crmValueString(row, "StageName"),
			Amount: crmValueFloat(row, "Amount"), Raw: row,
		}
		if record.ExternalID == "" {
			return
		}
		if err := s.upsertCRMRecord(ctx, tenantID, "salesforce", record); err == nil {
			synced++
		}
	}); err != nil {
		return synced, err
	}
	return synced, nil
}
