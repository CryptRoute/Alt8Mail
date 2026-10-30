package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const maxSignatures = 20

type signatureBody struct {
	Name           *string `json:"name"`
	BodyText       *string `json:"bodyText"`
	BodyHTML       *string `json:"bodyHtml"`
	DefaultNew     *bool   `json:"defaultNew"`
	DefaultReply   *bool   `json:"defaultReply"`
	DefaultForward *bool   `json:"defaultForward"`
}

type signatureRecord struct {
	ID, Name, BodyText, BodyHTML             string
	DefaultNew, DefaultReply, DefaultForward bool
	UpdatedAt                                time.Time
}

func (s *Server) listSignatures(ctx context.Context, user userRecord) ([]map[string]any, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, name, body_text, body_html, default_new, default_reply, default_forward, updated_at
		FROM mail_signatures WHERE user_id = $1 ORDER BY created_at
	`, user.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	signatures := []map[string]any{}
	for rows.Next() {
		var item signatureRecord
		if rows.Scan(&item.ID, &item.Name, &item.BodyText, &item.BodyHTML, &item.DefaultNew,
			&item.DefaultReply, &item.DefaultForward, &item.UpdatedAt) != nil {
			continue
		}
		signatures = append(signatures, signaturePayload(item))
	}
	return signatures, rows.Err()
}

func signaturePayload(item signatureRecord) map[string]any {
	return map[string]any{
		"id": item.ID, "name": item.Name, "bodyText": item.BodyText, "bodyHtml": item.BodyHTML,
		"defaultNew": item.DefaultNew, "defaultReply": item.DefaultReply, "defaultForward": item.DefaultForward,
		"updatedAt": item.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func signatureDefaults(signatures []map[string]any) map[string]any {
	defaults := map[string]any{"new": "", "reply": "", "forward": ""}
	for _, signature := range signatures {
		id, _ := signature["id"].(string)
		if signature["defaultNew"] == true && defaults["new"] == "" {
			defaults["new"] = id
		}
		if signature["defaultReply"] == true && defaults["reply"] == "" {
			defaults["reply"] = id
		}
		if signature["defaultForward"] == true && defaults["forward"] == "" {
			defaults["forward"] = id
		}
	}
	return defaults
}

func normalizeSignatureBodies(bodyText, bodyHTML string) (string, string, error) {
	bodyText = strings.TrimSpace(truncate(bodyText, 12_000))
	bodyHTML = sanitizeComposeHTML(truncate(bodyHTML, 24_000))
	if bodyHTML != "" && strings.TrimSpace(htmlToPlainText(bodyHTML)) == "" {
		bodyHTML = ""
	}
	if bodyText == "" && bodyHTML != "" {
		bodyText = htmlToPlainText(bodyHTML)
	}
	if bodyHTML == "" && bodyText != "" {
		bodyHTML = textToHTML(bodyText)
	}
	if bodyText == "" && bodyHTML == "" {
		return "", "", fail(http.StatusBadRequest, "SIGNATURE_EMPTY", "Write something in the signature before saving")
	}
	return bodyText, bodyHTML, nil
}

func (s *Server) getSignatures(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	signatures, err := s.listSignatures(r.Context(), sess.User)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SIGNATURES", "Could not load signatures")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"signatures": signatures, "count": len(signatures),
		"defaults": signatureDefaults(signatures), "max": maxSignatures,
	})
}

func (s *Server) createSignature(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body signatureBody
	if !s.decodeBody(w, r, &body, 128*1024) {
		return
	}
	name := ""
	if body.Name != nil {
		name = strings.TrimSpace(*body.Name)
	}
	if name == "" || len([]rune(name)) > 80 {
		writeError(w, http.StatusBadRequest, "SIGNATURE_NAME", "Give the signature a name of 1–80 characters")
		return
	}
	bodyText, bodyHTML := "", ""
	if body.BodyText != nil {
		bodyText = *body.BodyText
	}
	if body.BodyHTML != nil {
		bodyHTML = *body.BodyHTML
	}
	bodyText, bodyHTML, err := normalizeSignatureBodies(bodyText, bodyHTML)
	if err != nil {
		writeFromError(w, err)
		return
	}
	var count int
	_ = s.store.DB.QueryRow(r.Context(), `SELECT count(*) FROM mail_signatures WHERE user_id = $1`, sess.User.ID).Scan(&count)
	if count >= maxSignatures {
		writeError(w, http.StatusConflict, "SIGNATURE_LIMIT", "You can keep up to 20 signatures")
		return
	}
	defaultNew := count == 0
	defaultReply, defaultForward := false, false
	if body.DefaultNew != nil {
		defaultNew = *body.DefaultNew
	}
	if body.DefaultReply != nil {
		defaultReply = *body.DefaultReply
	}
	if body.DefaultForward != nil {
		defaultForward = *body.DefaultForward
	}
	id := newID("signature")
	ctx := r.Context()
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SIGNATURE", "Could not save the signature")
		return
	}
	defer tx.Rollback(ctx)
	if err := clearSignatureDefaults(ctx, tx, sess.User.ID, defaultNew, defaultReply, defaultForward); err != nil {
		writeError(w, http.StatusInternalServerError, "SIGNATURE", "Could not save the signature")
		return
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO mail_signatures (id, tenant_id, user_id, name, body_text, body_html,
		                             default_new, default_reply, default_forward)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`, id, sess.User.TenantID, sess.User.ID, name, bodyText, bodyHTML, defaultNew, defaultReply, defaultForward)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SIGNATURE", "Could not save the signature")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "SIGNATURE", "Could not save the signature")
		return
	}
	s.auditRequest(r, sess, "signature.created", name, "Created a named signature")
	item := signatureRecord{ID: id, Name: name, BodyText: bodyText, BodyHTML: bodyHTML,
		DefaultNew: defaultNew, DefaultReply: defaultReply, DefaultForward: defaultForward, UpdatedAt: time.Now().UTC()}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "signature": signaturePayload(item)})
}

func (s *Server) updateSignature(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body signatureBody
	if !s.decodeBody(w, r, &body, 128*1024) {
		return
	}
	ctx := r.Context()
	var item signatureRecord
	err := s.store.DB.QueryRow(ctx, `
		SELECT id, name, body_text, body_html, default_new, default_reply, default_forward, updated_at
		FROM mail_signatures WHERE id = $1 AND user_id = $2
	`, r.PathValue("id"), sess.User.ID).Scan(&item.ID, &item.Name, &item.BodyText, &item.BodyHTML,
		&item.DefaultNew, &item.DefaultReply, &item.DefaultForward, &item.UpdatedAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "SIGNATURE_NOT_FOUND", "Signature not found")
		return
	}
	if body.Name != nil {
		item.Name = strings.TrimSpace(*body.Name)
		if item.Name == "" || len([]rune(item.Name)) > 80 {
			writeError(w, http.StatusBadRequest, "SIGNATURE_NAME", "Give the signature a name of 1–80 characters")
			return
		}
	}
	if body.BodyText != nil {
		item.BodyText = *body.BodyText
	}
	if body.BodyHTML != nil {
		item.BodyHTML = *body.BodyHTML
	}
	item.BodyText, item.BodyHTML, err = normalizeSignatureBodies(item.BodyText, item.BodyHTML)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if body.DefaultNew != nil {
		item.DefaultNew = *body.DefaultNew
	}
	if body.DefaultReply != nil {
		item.DefaultReply = *body.DefaultReply
	}
	if body.DefaultForward != nil {
		item.DefaultForward = *body.DefaultForward
	}
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SIGNATURE", "Could not save the signature")
		return
	}
	defer tx.Rollback(ctx)
	if err := clearSignatureDefaults(ctx, tx, sess.User.ID, item.DefaultNew, item.DefaultReply, item.DefaultForward); err != nil {
		writeError(w, http.StatusInternalServerError, "SIGNATURE", "Could not save the signature")
		return
	}
	item.UpdatedAt = time.Now().UTC()
	_, err = tx.Exec(ctx, `
		UPDATE mail_signatures SET name=$2, body_text=$3, body_html=$4,
		       default_new=$5, default_reply=$6, default_forward=$7, updated_at=now()
		WHERE id=$1 AND user_id=$8
	`, item.ID, item.Name, item.BodyText, item.BodyHTML, item.DefaultNew, item.DefaultReply,
		item.DefaultForward, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SIGNATURE", "Could not save the signature")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "SIGNATURE", "Could not save the signature")
		return
	}
	s.auditRequest(r, sess, "signature.updated", item.Name, "Updated a named signature")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "signature": signaturePayload(item)})
}

func (s *Server) deleteSignature(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var name string
	err := s.store.DB.QueryRow(r.Context(), `
		DELETE FROM mail_signatures WHERE id=$1 AND user_id=$2 RETURNING name
	`, r.PathValue("id"), sess.User.ID).Scan(&name)
	if err != nil {
		writeError(w, http.StatusNotFound, "SIGNATURE_NOT_FOUND", "Signature not found")
		return
	}
	s.auditRequest(r, sess, "signature.deleted", name, "Deleted a named signature")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func clearSignatureDefaults(ctx context.Context, tx pgx.Tx, userID string, clearNew, clearReply, clearForward bool) error {
	for _, entry := range []struct {
		column string
		clear  bool
	}{
		{"default_new", clearNew},
		{"default_reply", clearReply},
		{"default_forward", clearForward},
	} {
		if !entry.clear {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE mail_signatures SET `+entry.column+` = false WHERE user_id = $1 AND `+entry.column, userID); err != nil {
			return err
		}
	}
	return nil
}
