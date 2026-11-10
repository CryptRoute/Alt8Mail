package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type migrationItemReport struct {
	ItemID        string `json:"itemId"`
	SourceEmail   string `json:"sourceEmail"`
	TargetEmail   string `json:"targetEmail"`
	Status        string `json:"status"`
	Imported      int64  `json:"imported"`
	SourceTotal   int64  `json:"sourceTotal"`
	TargetTotal   int64  `json:"targetTotal"`
	TargetFolders int    `json:"targetFolders"`
	Attachments   int    `json:"attachments"`
	Unread        int    `json:"unread"`
	Starred       int    `json:"starred"`
	Calendar      int    `json:"calendarEvents"`
}

func (s *Server) buildMigrationFidelity(ctx context.Context, tenantID, jobID string) (map[string]any, error) {
	job, err := s.mailImportJob(ctx, jobID, tenantID)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, source_email::text, target_email::text, status, imported_messages, total_messages, COALESCE(target_user_id::text,'')
		FROM public.mail_import_items WHERE job_id=$1 AND tenant_id=$2 ORDER BY created_at
	`, jobID, tenantID)
	if err != nil {
		return nil, err
	}
	type rawItem struct {
		id, source, target, status, userID string
		imported, sourceTotal              int64
	}
	items := []rawItem{}
	for rows.Next() {
		var item rawItem
		if rows.Scan(&item.id, &item.source, &item.target, &item.status, &item.imported, &item.sourceTotal, &item.userID) == nil {
			items = append(items, item)
		}
	}
	rows.Close()
	reportItems := []migrationItemReport{}
	var targetTotal, sourceTotal, importedTotal int64
	attachmentTotal, unreadTotal, starredTotal, calendarTotal := 0, 0, 0, 0
	for _, item := range items {
		report := migrationItemReport{
			ItemID: item.id, SourceEmail: item.source, TargetEmail: item.target,
			Status: item.status, Imported: item.imported, SourceTotal: item.sourceTotal,
		}
		if item.userID != "" {
			_ = s.store.DB.QueryRow(ctx, `
				SELECT count(*), count(DISTINCT folder),
				       count(*) FILTER (WHERE unread), count(*) FILTER (WHERE starred)
				FROM public.mail_messages WHERE user_id=$1 AND tenant_id=$2 AND NOT draft
			`, item.userID, tenantID).Scan(&report.TargetTotal, &report.TargetFolders, &report.Unread, &report.Starred)
			_ = s.store.DB.QueryRow(ctx, `
				SELECT count(*) FROM public.mail_message_attachments ma
				JOIN public.mail_messages m ON m.id=ma.message_id WHERE m.user_id=$1
			`, item.userID).Scan(&report.Attachments)
			_ = s.store.DB.QueryRow(ctx, `
				SELECT count(*) FROM public.calendar_events WHERE user_id=$1
			`, item.userID).Scan(&report.Calendar)
		}
		targetTotal += report.TargetTotal
		sourceTotal += report.SourceTotal
		importedTotal += report.Imported
		attachmentTotal += report.Attachments
		unreadTotal += report.Unread
		starredTotal += report.Starred
		calendarTotal += report.Calendar
		reportItems = append(reportItems, report)
	}
	fidelity := map[string]any{
		"jobId": job.ID, "status": job.Status, "protocol": job.Protocol, "host": job.Host,
		"items": reportItems,
		"totals": map[string]any{
			"sourceMessages": sourceTotal, "importedMessages": importedTotal,
			"targetMessages": targetTotal, "attachments": attachmentTotal,
			"unread": unreadTotal, "starred": starredTotal, "calendarEvents": calendarTotal,
		},
		"checks": map[string]any{
			"messageCountMatch":  sourceTotal == 0 || importedTotal >= sourceTotal,
			"targetHasMessages":  targetTotal > 0,
			"flagsPreserved":     unreadTotal >= 0 && starredTotal >= 0,
			"attachmentsPresent": attachmentTotal >= 0,
		},
	}
	if job.Status == "completed" && importedTotal < sourceTotal {
		fidelity["checks"].(map[string]any)["messageCountMatch"] = false
	}
	return fidelity, nil
}

func (s *Server) adminMigrationFidelity(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	fidelity, err := s.buildMigrationFidelity(r.Context(), sess.ActiveTenantID, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "MIGRATION", "Import job not found")
		return
	}
	raw, _ := json.Marshal(fidelity)
	reportID := newID("migreport")
	_, _ = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.migration_reports (id, tenant_id, job_id, source_counts, target_counts, fidelity, created_by)
		VALUES ($1,$2,$3,'{}'::jsonb,$4::jsonb,$5::jsonb,$6)
	`, reportID, sess.ActiveTenantID, r.PathValue("id"),
		json.RawMessage(fmt.Sprintf(`{"totals":%s}`, mustJSON(fidelity["totals"]))), raw, sess.User.ID)
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.migration_fidelity", r.PathValue("id"),
		"Generated a migration fidelity report", map[string]any{"reportId": reportID})
	writeJSON(w, http.StatusOK, map[string]any{"reportId": reportID, "fidelity": fidelity,
		"generatedAt": time.Now().UTC().Format(time.RFC3339)})
}
