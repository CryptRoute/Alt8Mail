package httpapi

import (
	"context"
	"strings"
	"time"
)

// mailImportEvent records a durable, admin-visible migration event. It uses its
// own short context so a cancelled import still leaves the failure reason in the
// job history.
func (s *Server) mailImportEvent(ctx context.Context, jobID, itemID, level, stage, message, detail string) {
	if s == nil || s.store == nil || s.store.DB == nil || jobID == "" {
		return
	}
	_ = ctx
	level = strings.ToLower(strings.TrimSpace(level))
	if level != "warn" && level != "error" {
		level = "info"
	}
	stage = truncate(sanitizeMailText(strings.TrimSpace(stage)), 80)
	message = truncate(sanitizeMailText(strings.TrimSpace(message)), 500)
	detail = truncate(sanitizeMailText(strings.TrimSpace(detail)), 2000)
	eventCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = s.store.DB.Exec(eventCtx, `
		INSERT INTO mail_import_events (id, job_id, item_id, level, stage, message, detail)
		VALUES ($1,$2,NULLIF($3,''),$4,$5,$6,$7)
	`, newID("mimpe"), jobID, itemID, level, stage, message, detail)
}
