package httpapi

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
	"time"
)

func TestTaskRecurrenceNextOccurrence(t *testing.T) {
	base := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC) // Monday
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	next, ok := taskNextOccurrence("FREQ=DAILY;INTERVAL=1", base, now, 0)
	if !ok || !next.Equal(base.AddDate(0, 0, 1)) {
		t.Fatalf("daily next = %v ok=%v", next, ok)
	}
	next, ok = taskNextOccurrence("FREQ=WEEKLY;BYDAY=MO,WE", base, now, 0)
	if !ok || next.Weekday() != time.Wednesday {
		t.Fatalf("weekly next = %v ok=%v", next, ok)
	}
	next, ok = taskNextOccurrence("FREQ=MONTHLY;INTERVAL=1", base, now, 0)
	if !ok || !next.Equal(base.AddDate(0, 1, 0)) {
		t.Fatalf("monthly next = %v ok=%v", next, ok)
	}
	if _, ok := taskNextOccurrence("FREQ=DAILY;COUNT=3", base, now, 3); ok {
		t.Fatal("count-limited recurrence exceeded COUNT")
	}
	if _, ok := taskNextOccurrence("", base, now, 0); ok {
		t.Fatal("empty recurrence produced an occurrence")
	}
}

func TestAPICursorRoundTrip(t *testing.T) {
	stamp := time.Date(2026, 11, 10, 12, 30, 0, 123456789, time.UTC)
	cursor := encodeAPICursor(stamp, "task_abc-123")
	gotStamp, gotID, ok := decodeAPICursor(cursor)
	if !ok || gotID != "task_abc-123" || !gotStamp.Equal(stamp) {
		t.Fatalf("cursor round trip = %v %q %v", gotStamp, gotID, ok)
	}
	if _, _, ok := decodeAPICursor("not-a-cursor"); ok {
		t.Fatal("invalid cursor accepted")
	}
}

func TestDMARCReportArchives(t *testing.T) {
	xml := `<?xml version="1.0"?><feedback><report_metadata><org_name>Example</org_name><report_id>r1</report_id><date_range><begin>1700000000</begin><end>1700086400</end></date_range></report_metadata><policy_published><domain>example.test</domain><p>none</p></policy_published><record><row><source_ip>203.0.113.5</source_ip><count>2</count><policy_evaluated><disposition>none</disposition><dkim>pass</dkim><spf>fail</spf></policy_evaluated></row><identifiers><header_from>example.test</header_from></identifiers><auth_results><dkim><result>pass</result></dkim><spf><result>fail</result></spf></auth_results></record></feedback>`
	var zipped bytes.Buffer
	writer := zip.NewWriter(&zipped)
	file, err := writer.Create("report.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte(xml))
	_ = writer.Close()
	feedback, err := parseDMARCReport(zipped.Bytes())
	if err != nil || feedback.PolicyPublished.Domain != "example.test" || len(feedback.Records) != 1 {
		t.Fatalf("zip DMARC = %#v err=%v", feedback, err)
	}
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	_, _ = gz.Write([]byte(xml))
	_ = gz.Close()
	if feedback, err := parseDMARCReport(compressed.Bytes()); err != nil || feedback.ReportMetadata.ReportID != "r1" {
		t.Fatalf("gzip DMARC = %#v err=%v", feedback, err)
	}
	if !dmarcAttachmentCandidate("report.xml.gz", "") || !dmarcAttachmentCandidate("report.zip", "") || dmarcAttachmentCandidate("readme.txt", "text/plain") {
		t.Fatal("DMARC attachment filter is wrong")
	}
}

func TestCalDAVFreeBusyParsing(t *testing.T) {
	start := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 2)
	raw := []byte("BEGIN:VCALENDAR\r\nBEGIN:VFREEBUSY\r\nFREEBUSY:20261110T090000Z/20261110T100000Z\r\nFREEBUSY;FBTYPE=BUSY:20261111T140000Z/20261111T150000Z\r\nFREEBUSY;FBTYPE=FREE:20261111T160000Z/20261111T170000Z\r\nEND:VFREEBUSY\r\nEND:VCALENDAR\r\n")
	busy, err := parseCalDAVFreeBusy(raw, start, end)
	if err != nil || len(busy) != 2 {
		t.Fatalf("CalDAV busy = %v err=%v", busy, err)
	}
	if !strings.Contains(busy[0]["start"].(string), "09:00:00") {
		t.Fatalf("first interval = %v", busy[0])
	}
}

func TestMCPToolApprovalPolicyValidation(t *testing.T) {
	policy := defaultTenantPolicy()
	policy.MCP.ToolApprovals["draft_send"] = "deny"
	policy.MCP.ToolApprovals["tasks_write"] = "auto"
	policy.MCP.ConnectionRequestsPerHour = 30
	validated, errors := validateTenantPolicy(policy)
	if len(errors) != 0 {
		t.Fatalf("policy errors = %v", errors)
	}
	if validated.MCP.ToolApprovals["draft_send"] != "deny" || validated.MCP.ToolApprovals["tasks_write"] != "auto" {
		t.Fatalf("tool approvals = %#v", validated.MCP.ToolApprovals)
	}
	if validated.MCP.ToolApprovals["calendar_write"] != "approval" {
		t.Fatalf("default tool approval = %q", validated.MCP.ToolApprovals["calendar_write"])
	}
	policy.MCP.ToolApprovals["draft_send"] = "sometimes"
	if _, errors := validateTenantPolicy(policy); len(errors) == 0 {
		t.Fatal("invalid tool approval accepted")
	}
}
