package httpapi

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type dmarcFeedback struct {
	ReportMetadata struct {
		OrgName   string `xml:"org_name"`
		ReportID  string `xml:"report_id"`
		DateRange struct {
			Begin int64 `xml:"begin"`
			End   int64 `xml:"end"`
		} `xml:"date_range"`
	} `xml:"report_metadata"`
	PolicyPublished struct {
		Domain string `xml:"domain"`
		Policy string `xml:"p"`
	} `xml:"policy_published"`
	Records []struct {
		Row struct {
			SourceIP string `xml:"source_ip"`
			Count    int64  `xml:"count"`
			Policy   struct {
				Disposition string `xml:"disposition"`
				DKIM        string `xml:"dkim"`
				SPF         string `xml:"spf"`
			} `xml:"policy_evaluated"`
		} `xml:"row"`
		Identifiers struct {
			HeaderFrom string `xml:"header_from"`
		} `xml:"identifiers"`
		AuthResults struct {
			DKIM struct {
				Result string `xml:"result"`
			} `xml:"dkim"`
			SPF struct {
				Result string `xml:"result"`
			} `xml:"spf"`
		} `xml:"auth_results"`
	} `xml:"record"`
}

func dmarcReportXML(raw []byte) ([]byte, error) {
	if len(raw) > 4 && bytes.Equal(raw[:4], []byte{'P', 'K', 3, 4}) {
		reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			return nil, fmt.Errorf("DMARC report ZIP is unreadable")
		}
		for _, file := range reader.File {
			if !strings.HasSuffix(strings.ToLower(file.Name), ".xml") && !strings.HasSuffix(strings.ToLower(file.Name), ".xml.gz") {
				continue
			}
			opened, err := file.Open()
			if err != nil {
				continue
			}
			data, err := io.ReadAll(io.LimitReader(opened, 8*1024*1024+1))
			opened.Close()
			if err != nil || len(data) > 8*1024*1024 {
				return nil, fmt.Errorf("DMARC report is too large")
			}
			return dmarcReportXML(data)
		}
		return nil, fmt.Errorf("DMARC report ZIP contains no XML report")
	}
	if len(raw) > 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		reader, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("DMARC report is not readable gzip")
		}
		defer reader.Close()
		data, err := io.ReadAll(io.LimitReader(reader, 8*1024*1024+1))
		if err != nil || len(data) > 8*1024*1024 {
			return nil, fmt.Errorf("DMARC report is too large")
		}
		return dmarcReportXML(data)
	}
	return raw, nil
}

func parseDMARCReport(raw []byte) (dmarcFeedback, error) {
	var feedback dmarcFeedback
	var err error
	raw, err = dmarcReportXML(raw)
	if err != nil {
		return feedback, err
	}
	if len(raw) == 0 || len(raw) > 8*1024*1024 {
		return feedback, fmt.Errorf("DMARC report is empty or too large")
	}
	if err := xml.Unmarshal(raw, &feedback); err != nil {
		return feedback, fmt.Errorf("DMARC report XML is invalid")
	}
	if strings.TrimSpace(feedback.PolicyPublished.Domain) == "" || strings.TrimSpace(feedback.ReportMetadata.ReportID) == "" {
		return feedback, fmt.Errorf("DMARC report is missing a domain or report id")
	}
	if feedback.ReportMetadata.DateRange.Begin <= 0 || feedback.ReportMetadata.DateRange.End <= 0 {
		return feedback, fmt.Errorf("DMARC report is missing its date range")
	}
	if len(feedback.Records) > 5000 {
		return feedback, fmt.Errorf("DMARC report has too many records")
	}
	return feedback, nil
}

type dmarcIngestResult struct {
	ReportID  string
	Duplicate bool
	Messages  int64
	Pass      int64
	Fail      int64
}

func (s *Server) ingestDMARCReport(ctx context.Context, tenantID string, raw []byte) (dmarcIngestResult, error) {
	feedback, err := parseDMARCReport(raw)
	if err != nil {
		return dmarcIngestResult{}, err
	}
	domain := normalizeEmailValue(feedback.PolicyPublished.Domain)
	var exists bool
	_ = s.store.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.mail_domains WHERE tenant_id=$1 AND domain=$2)`, tenantID, domain).Scan(&exists)
	if !exists {
		return dmarcIngestResult{}, fmt.Errorf("domain %s is not configured for this organization", domain)
	}
	reportID := newID("dmarc")
	var messages, passed, failed int64
	records := []map[string]any{}
	for _, record := range feedback.Records {
		count := record.Row.Count
		if count <= 0 {
			continue
		}
		messages += count
		pass := strings.EqualFold(record.Row.Policy.DKIM, "pass") || strings.EqualFold(record.Row.Policy.SPF, "pass")
		if pass {
			passed += count
		} else {
			failed += count
		}
		sourceIP := strings.TrimSpace(record.Row.SourceIP)
		if net.ParseIP(sourceIP) == nil {
			sourceIP = ""
		}
		records = append(records, map[string]any{
			"sourceIp": sourceIP, "count": count,
			"disposition": strings.ToLower(strings.TrimSpace(record.Row.Policy.Disposition)),
			"dkim":        strings.ToLower(strings.TrimSpace(record.Row.Policy.DKIM)),
			"spf":         strings.ToLower(strings.TrimSpace(record.Row.Policy.SPF)),
			"headerFrom":  strings.TrimSpace(record.Identifiers.HeaderFrom),
			"alignedDkim": strings.ToLower(strings.TrimSpace(record.AuthResults.DKIM.Result)),
			"alignedSpf":  strings.ToLower(strings.TrimSpace(record.AuthResults.SPF.Result)),
		})
	}
	summary, _ := json.Marshal(map[string]any{
		"messages": messages, "passed": passed, "failed": failed,
		"passRate": func() float64 {
			if messages == 0 {
				return 0
			}
			return float64(passed) / float64(messages) * 100
		}(),
		"policy": feedback.PolicyPublished.Policy,
	})
	begin := time.Unix(feedback.ReportMetadata.DateRange.Begin, 0).UTC()
	end := time.Unix(feedback.ReportMetadata.DateRange.End, 0).UTC()
	var insertedID string
	err = s.store.DB.QueryRow(ctx, `
		INSERT INTO public.dmarc_reports
		  (id, tenant_id, domain, report_id, org_name, range_begin, range_end, policy,
		   records_count, messages_count, pass_count, fail_count, summary, raw_xml)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::jsonb,$14)
		ON CONFLICT (tenant_id, domain, report_id, org_name) DO NOTHING
		RETURNING id
	`, reportID, tenantID, domain, truncate(feedback.ReportMetadata.ReportID, 200),
		truncate(feedback.ReportMetadata.OrgName, 200), begin, end, truncate(feedback.PolicyPublished.Policy, 20),
		len(records), messages, passed, failed, summary, string(raw)).Scan(&insertedID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no rows") {
			return dmarcIngestResult{ReportID: feedback.ReportMetadata.ReportID, Duplicate: true}, nil
		}
		return dmarcIngestResult{}, err
	}
	for _, record := range records {
		_, _ = s.store.DB.Exec(ctx, `
			INSERT INTO public.dmarc_records
			  (report_id, source_ip, count, disposition, dkim_result, spf_result, header_from, aligned_dkim, aligned_spf)
			VALUES ($1,NULLIF($2,'')::inet,$3,$4,$5,$6,$7,$8,$9)
		`, insertedID, record["sourceIp"], record["count"], record["disposition"], record["dkim"],
			record["spf"], record["headerFrom"], record["alignedDkim"], record["alignedSpf"])
	}
	return dmarcIngestResult{ReportID: feedback.ReportMetadata.ReportID, Messages: messages, Pass: passed, Fail: failed}, nil
}

func (s *Server) adminIngestDMARC(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8*1024*1024+1))
	if err != nil || len(raw) > 8*1024*1024 {
		writeError(w, http.StatusBadRequest, "DMARC", "DMARC report is too large")
		return
	}
	result, err := s.ingestDMARCReport(r.Context(), sess.ActiveTenantID, raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "DMARC", err.Error())
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "dmarc.report_ingested", result.ReportID, "Ingested DMARC aggregate report", map[string]any{"duplicate": result.Duplicate, "messages": result.Messages, "failed": result.Fail})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "report": result})
}

func (s *Server) adminDMARCSummary(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	days, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("days")))
	if days <= 0 || days > 365 {
		days = 30
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT domain::text, sum(messages_count), sum(pass_count), sum(fail_count), count(*)
		FROM public.dmarc_reports
		WHERE tenant_id=$1 AND range_begin > now() - ($2 * interval '1 day')
		GROUP BY domain ORDER BY domain
	`, sess.ActiveTenantID, days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DMARC", "Could not load DMARC summary")
		return
	}
	defer rows.Close()
	domains := []map[string]any{}
	var total, pass, fail int64
	for rows.Next() {
		var domain string
		var messages, passed, failed, reports int64
		if rows.Scan(&domain, &messages, &passed, &failed, &reports) == nil {
			total += messages
			pass += passed
			fail += failed
			rate := 0.0
			if messages > 0 {
				rate = float64(passed) / float64(messages) * 100
			}
			domains = append(domains, map[string]any{"domain": domain, "messages": messages, "pass": passed, "fail": failed, "passRate": rate, "reports": reports, "alert": failed > 0 && rate < 90})
		}
	}
	failingRows, _ := s.store.DB.Query(r.Context(), `
		SELECT m.domain::text, r.source_ip::text, r.count, r.disposition
		FROM public.dmarc_records r
		JOIN public.dmarc_reports m ON m.id=r.report_id
		WHERE m.tenant_id=$1 AND m.range_begin > now() - ($2 * interval '1 day')
		  AND r.dkim_result <> 'pass' AND r.spf_result <> 'pass'
		ORDER BY r.count DESC LIMIT 20
	`, sess.ActiveTenantID, days)
	failing := []map[string]any{}
	if failingRows != nil {
		for failingRows.Next() {
			var domain, sourceIP, disposition string
			var count int64
			if failingRows.Scan(&domain, &sourceIP, &count, &disposition) == nil {
				failing = append(failing, map[string]any{"domain": domain, "sourceIp": sourceIP, "count": count, "disposition": disposition})
			}
		}
		failingRows.Close()
	}
	rate := 0.0
	if total > 0 {
		rate = float64(pass) / float64(total) * 100
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"days": days, "domains": domains, "failingSources": failing,
		"totals": map[string]any{"messages": total, "pass": pass, "fail": fail, "passRate": rate},
	})
}

func (s *Server) registerDMARCRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /dmarc/ingest", s.adminFeature("domains.manage", FeatureDeliverability, s.adminIngestDMARC))
	mux.HandleFunc("GET /dmarc/summary", s.adminFeature("domains.read", FeatureDeliverability, s.adminDMARCSummary))
	mux.HandleFunc("GET /dmarc/poller", s.adminFeature("domains.read", FeatureDeliverability, s.adminDMARCPoller))
	mux.HandleFunc("PUT /dmarc/poller", s.adminFeature("domains.manage", FeatureDeliverability, s.adminSaveDMARCPoller))
	mux.HandleFunc("POST /dmarc/poller/run", s.adminFeature("domains.manage", FeatureDeliverability, s.adminRunDMARCPoller))
}

// parseDMARCDate is used by tests and future report exporters.
func parseDMARCDate(value int64) time.Time { return time.Unix(value, 0).UTC() }
