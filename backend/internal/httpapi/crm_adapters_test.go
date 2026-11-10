package httpapi

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCRMProviderAdaptersIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the CRM adapter test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4614-8000-%012d", time.Now().UnixNano()%1000000000000)
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'CRM Adapters')`, tenant, "crm-adapters-"+suffix)
	server := &Server{store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		stateLocks: map[string]*sync.Mutex{}, cfg: config.Config{SessionSecret: "crm-adapters-secret"}}
	connect := func(provider, credentials string) {
		t.Helper()
		sealed, err := server.sealWorkspaceToken(credentials)
		if err != nil {
			t.Fatal(err)
		}
		mustExec(`INSERT INTO public.addon_connections (id, tenant_id, addon_id, connection_scope, credentials_sealed, status)
			VALUES ($1,$2,$3,'tenant',$4,'active')`, newID("conn"), tenant, provider, sealed)
	}
	countRecords := func(provider string) int {
		var count int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.crm_records WHERE tenant_id=$1 AND provider=$2`, tenant, provider).Scan(&count)
		return count
	}

	t.Run("pipedrive", func(t *testing.T) {
		fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("x-api-token") != "pd-token" {
				http.Error(w, "bad token", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v1/persons":
				_, _ = io.WriteString(w, `{"success":true,"data":[
					{"id":11,"name":"Ada Lovelace","org_name":"Analytical","email":[{"value":"ada@client.example.test","primary":true}]},
					{"id":12,"name":"Grace Hopper","org_name":"Navy","email":[{"value":"grace@client.example.test","primary":true}]}
				]}`)
			case "/v1/deals":
				_, _ = io.WriteString(w, `{"success":true,"data":[
					{"id":21,"title":"Analytical renewal","value":9000,"stage_id":3,"person_id":{"email":[{"value":"ada@client.example.test","primary":true}]}}
				]}`)
			default:
				http.NotFound(w, r)
			}
		}))
		defer fake.Close()
		server.pipedriveBaseURL = fake.URL
		connect("pipedrive", `{"apiToken":"pd-token"}`)
		count, err := server.syncCRM(ctx, tenant, "pipedrive")
		if err != nil || count != 3 {
			t.Fatalf("pipedrive sync count=%d err=%v", count, err)
		}
		if got := countRecords("pipedrive"); got != 3 {
			t.Fatalf("pipedrive rows = %d", got)
		}
	})

	t.Run("zoho", func(t *testing.T) {
		fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/oauth/v2/token":
				if err := r.ParseForm(); err != nil || r.Form.Get("refresh_token") != "zoho-refresh" {
					http.Error(w, "bad refresh", http.StatusUnauthorized)
					return
				}
				_, _ = io.WriteString(w, `{"access_token":"zoho-access","api_domain":"`+fakeURL(r)+`"}`)
			case "/crm/v5/Contacts":
				if r.Header.Get("Authorization") != "Zoho-oauthtoken zoho-access" {
					http.Error(w, "bad auth", http.StatusUnauthorized)
					return
				}
				_, _ = io.WriteString(w, `{"data":[{"id":"zc-1","Email":"zoe@client.example.test","Full_Name":"Zoe Z","Company":"Zeta","Title":"VP","Lead_Status":"Contacted"}],"info":{"more_records":false}}`)
			case "/crm/v5/Deals":
				_, _ = io.WriteString(w, `{"data":[{"id":"zd-1","Deal_Name":"Zeta expansion","Amount":12000,"Stage":"Proposal"}],"info":{"more_records":false}}`)
			default:
				http.NotFound(w, r)
			}
		}))
		defer fake.Close()
		server.zohoAccountsBaseURL = fake.URL
		server.zohoCRMBaseURL = fake.URL
		connect("zoho", `{"region":"com","clientId":"zoho-client","clientSecret":"zoho-secret","refreshToken":"zoho-refresh"}`)
		count, err := server.syncCRM(ctx, tenant, "zoho")
		if err != nil || count != 2 {
			t.Fatalf("zoho sync count=%d err=%v", count, err)
		}
		if got := countRecords("zoho"); got != 2 {
			t.Fatalf("zoho rows = %d", got)
		}
	})

	t.Run("salesforce", func(t *testing.T) {
		fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch {
			case r.URL.Path == "/services/oauth2/token":
				if err := r.ParseForm(); err != nil || r.Form.Get("refresh_token") != "sf-refresh" {
					http.Error(w, "bad refresh", http.StatusUnauthorized)
					return
				}
				_, _ = io.WriteString(w, `{"access_token":"sf-access","instance_url":"`+fakeURL(r)+`"}`)
			case strings.HasPrefix(r.URL.Path, "/services/data/v60.0/query"):
				if r.Header.Get("Authorization") != "Bearer sf-access" {
					http.Error(w, "bad auth", http.StatusUnauthorized)
					return
				}
				switch {
				case strings.Contains(r.URL.Query().Get("q"), "FROM Contact"):
					_, _ = io.WriteString(w, `{"done":true,"records":[{"Id":"003A","Email":"sam@client.example.test","Name":"Sam Smith","Title":"Director","LeadSource":"Web","Account":{"Name":"Sunrise"}}]}`)
				case strings.Contains(r.URL.Query().Get("q"), "FROM Opportunity"):
					_, _ = io.WriteString(w, `{"done":true,"records":[{"Id":"006A","Name":"Sunrise renewal","Amount":45000,"StageName":"Negotiation"}]}`)
				default:
					http.NotFound(w, r)
				}
			default:
				http.NotFound(w, r)
			}
		}))
		defer fake.Close()
		server.salesforceBaseURL = fake.URL
		connect("salesforce", `{"loginUrl":"https://login.salesforce.com","clientId":"sf-client","clientSecret":"sf-secret","refreshToken":"sf-refresh"}`)
		count, err := server.syncCRM(ctx, tenant, "salesforce")
		if err != nil || count != 2 {
			t.Fatalf("salesforce sync count=%d err=%v", count, err)
		}
		if got := countRecords("salesforce"); got != 2 {
			t.Fatalf("salesforce rows = %d", got)
		}
	})
}

func fakeURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func TestSalesforceLoginURLValidation(t *testing.T) {
	for _, valid := range []string{"https://login.salesforce.com", "https://test.salesforce.com/"} {
		if !salesforceLoginAllowed(valid) {
			t.Fatalf("expected %q to be allowed", valid)
		}
	}
	for _, invalid := range []string{"http://login.salesforce.com", "https://evil.example.com", "https://login.salesforce.com.evil.test", "https://login.salesforce.com/path"} {
		if salesforceLoginAllowed(invalid) {
			t.Fatalf("expected %q to be rejected", invalid)
		}
	}
}
