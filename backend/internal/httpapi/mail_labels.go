package httpapi

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const maxMailLabels = 50

var mailLabelColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

type mailLabelRecord struct {
	ID        string
	Name      string
	Color     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (label mailLabelRecord) public() map[string]any {
	return map[string]any{
		"id": label.ID, "name": label.Name, "color": label.Color,
		"createdAt": label.CreatedAt.UTC().Format(time.RFC3339),
		"updatedAt": label.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func (s *Server) listMailLabels(ctx context.Context, userID string) ([]mailLabelRecord, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, name, color, created_at, updated_at
		FROM mail_labels WHERE user_id=$1 ORDER BY lower(name)
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	labels := []mailLabelRecord{}
	for rows.Next() {
		var label mailLabelRecord
		if rows.Scan(&label.ID, &label.Name, &label.Color, &label.CreatedAt, &label.UpdatedAt) == nil {
			labels = append(labels, label)
		}
	}
	return labels, rows.Err()
}

func (s *Server) getMailLabels(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	labels, err := s.listMailLabels(r.Context(), sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LABELS", "Could not load labels")
		return
	}
	payload := make([]any, 0, len(labels))
	for _, label := range labels {
		payload = append(payload, label.public())
	}
	writeJSON(w, http.StatusOK, map[string]any{"labels": payload, "count": len(payload), "max": maxMailLabels})
}

type mailLabelBody struct {
	Name  *string `json:"name"`
	Color *string `json:"color"`
}

func cleanLabelName(value string) (string, bool) {
	name := strings.TrimSpace(value)
	if name == "" || len([]rune(name)) > 40 {
		return "", false
	}
	return name, true
}

func cleanLabelColor(value string) (string, bool) {
	color := strings.TrimSpace(value)
	if color == "" {
		return "#0f6cbd", true
	}
	if !mailLabelColorPattern.MatchString(color) {
		return "", false
	}
	return strings.ToLower(color), true
}

func (s *Server) createMailLabel(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body mailLabelBody
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	name, ok := cleanLabelName(*body.Name)
	if body.Name == nil || !ok {
		writeError(w, http.StatusBadRequest, "LABEL_NAME", "Give the label a name of 1–40 characters")
		return
	}
	color, ok := cleanLabelColor(valueOrEmpty(body.Color))
	if !ok {
		writeError(w, http.StatusBadRequest, "LABEL_COLOR", "Choose a six-digit hex colour")
		return
	}
	var count int
	_ = s.store.DB.QueryRow(r.Context(), `SELECT count(*) FROM mail_labels WHERE user_id=$1`, sess.User.ID).Scan(&count)
	if count >= maxMailLabels {
		writeError(w, http.StatusConflict, "LABEL_LIMIT", "You can keep up to 50 labels")
		return
	}
	id := newID("label")
	var record mailLabelRecord
	err := s.store.DB.QueryRow(r.Context(), `
		INSERT INTO mail_labels (id, tenant_id, user_id, name, color)
		VALUES ($1,$2,$3,$4,$5)
		RETURNING id, name, color, created_at, updated_at
	`, id, sess.User.TenantID, sess.User.ID, name, color).Scan(
		&record.ID, &record.Name, &record.Color, &record.CreatedAt, &record.UpdatedAt)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			writeError(w, http.StatusConflict, "LABEL_EXISTS", "A label with that name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "LABEL", "Could not create the label")
		return
	}
	s.auditRequest(r, sess, "label.created", name, "Created a message label")
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "label": record.public()})
}

func (s *Server) updateMailLabel(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body mailLabelBody
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	var record mailLabelRecord
	err := s.store.DB.QueryRow(r.Context(), `
		SELECT id, name, color, created_at, updated_at
		FROM mail_labels WHERE id=$1 AND user_id=$2
	`, r.PathValue("id"), sess.User.ID).Scan(&record.ID, &record.Name, &record.Color, &record.CreatedAt, &record.UpdatedAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "LABEL_NOT_FOUND", "Label not found")
		return
	}
	if body.Name != nil {
		name, ok := cleanLabelName(*body.Name)
		if !ok {
			writeError(w, http.StatusBadRequest, "LABEL_NAME", "Give the label a name of 1–40 characters")
			return
		}
		record.Name = name
	}
	if body.Color != nil {
		color, ok := cleanLabelColor(*body.Color)
		if !ok {
			writeError(w, http.StatusBadRequest, "LABEL_COLOR", "Choose a six-digit hex colour")
			return
		}
		record.Color = color
	}
	err = s.store.DB.QueryRow(r.Context(), `
		UPDATE mail_labels SET name=$3, color=$4, updated_at=now()
		WHERE id=$1 AND user_id=$2
		RETURNING updated_at
	`, record.ID, sess.User.ID, record.Name, record.Color).Scan(&record.UpdatedAt)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			writeError(w, http.StatusConflict, "LABEL_EXISTS", "A label with that name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "LABEL", "Could not update the label")
		return
	}
	s.auditRequest(r, sess, "label.updated", record.Name, "Updated a message label")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "label": record.public()})
}

func (s *Server) deleteMailLabel(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	var name string
	if err := s.store.DB.QueryRow(r.Context(), `
		DELETE FROM mail_labels WHERE id=$1 AND user_id=$2 RETURNING name
	`, id, sess.User.ID).Scan(&name); err != nil {
		writeError(w, http.StatusNotFound, "LABEL_NOT_FOUND", "Label not found")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `
		UPDATE mail_messages SET categories=array_remove(categories,$2), updated_at=now()
		WHERE user_id=$1 AND $2=ANY(categories)
	`, sess.User.ID, id)
	s.auditRequest(r, sess, "label.deleted", name, "Deleted a message label")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
