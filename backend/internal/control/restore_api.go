package control

import (
	"net/http"
	"strings"
)

func (s *Server) handleMailboxRestorePreview(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var req mailboxRestoreRequest
	if err := decodeJSON(w, r, &req, 32<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	backupID := strings.TrimSpace(r.PathValue("id"))
	preview, target, name, err := s.previewMailboxRestore(r.Context(), backupID, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "RESTORE", err.Error())
		return
	}
	jobID, err := s.createRestoreJob(r.Context(), backupID, target, "preview", preview, sess.User.ID)
	if err == nil {
		s.updateRestoreJob(jobID, "completed", preview, "Preview only; no live data changed")
	}
	s.actorAudit(r, sess, "backup.restore_previewed", target.Email, "Previewed a mailbox restore", map[string]any{
		"backup": name, "folder": target.Folder, "messages": preview.Messages, "conflicts": preview.Conflicts,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "jobId": jobID, "backup": name, "preview": preview})
}

func (s *Server) handleMailboxRestoreApply(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var req mailboxRestoreRequest
	if err := decodeJSON(w, r, &req, 32<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	backupID := strings.TrimSpace(r.PathValue("id"))
	preview, target, name, err := s.previewMailboxRestore(r.Context(), backupID, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "RESTORE", err.Error())
		return
	}
	jobID, err := s.createRestoreJob(r.Context(), backupID, target, "apply", preview, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "RESTORE", "Could not create the restore job")
		return
	}
	s.actorAudit(r, sess, "backup.restore_started", target.Email, "Started a mailbox restore", map[string]any{
		"backup": name, "folder": target.Folder, "messages": preview.Messages, "conflicts": preview.Conflicts,
	})
	go s.runMailboxRestore(jobID, backupID, req, &sess.User)
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "jobId": jobID, "backup": name, "preview": preview})
}

func (s *Server) handleRestoreJob(w http.ResponseWriter, r *http.Request, _ *sessionRecord) {
	job, err := s.loadRestoreJob(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "RESTORE_JOB", "Restore job not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": job})
}
