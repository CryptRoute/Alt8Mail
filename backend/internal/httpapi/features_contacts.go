package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type contactBody struct {
	Name     *string `json:"name"`
	Email    *string `json:"email"`
	Company  *string `json:"company"`
	Title    *string `json:"title"`
	Phone    *string `json:"phone"`
	Location *string `json:"location"`
	Notes    *string `json:"notes"`
}

func (s *Server) getDirectory(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	people, err := s.directoryPeople(r.Context(), sess, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DIRECTORY", "Could not load directory")
		return
	}
	policy := s.tenantPolicyFor(r.Context(), sess.User.TenantID)
	writeJSON(w, http.StatusOK, map[string]any{
		"people": people, "count": len(people),
		"policy": map[string]any{
			"contactSharing":    policy.Directory.ContactSharing || webmailAdminRole(sess.User.Role),
			"externalDirectory": policy.Directory.ExternalDirectory,
			"profileEditing":    policy.Directory.ProfileEditing,
		},
	})
}

func (s *Server) getContacts(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	contacts, err := s.loadContacts(r.Context(), sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CONTACTS", "Could not load contacts")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"contacts": contacts, "count": len(contacts)})
}

func (s *Server) loadContacts(ctx context.Context, userID string) ([]map[string]any, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, email::text, name, company, title, phone, location, notes, source, created_at, updated_at
		FROM contacts WHERE user_id = $1 ORDER BY name, email
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	contacts := []map[string]any{}
	for rows.Next() {
		var id, email, name, company, title, phone, location, notes, source string
		var created, updated time.Time
		if rows.Scan(&id, &email, &name, &company, &title, &phone, &location, &notes, &source, &created, &updated) == nil {
			contacts = append(contacts, map[string]any{
				"id": id, "email": strings.ToLower(email), "name": firstNonEmpty(name, email),
				"company": company, "title": title, "phone": phone, "location": location,
				"notes": notes, "source": source,
				"initials": initialsForName(firstNonEmpty(name, email)), "color": colorForString(email),
				"createdAt": created.UTC().Format(time.RFC3339), "updatedAt": updated.UTC().Format(time.RFC3339),
			})
		}
	}
	return contacts, rows.Err()
}

func (s *Server) createContact(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body contactBody
	if !s.decodeBody(w, r, &body, 256*1024) {
		return
	}
	email, name, company, title, phone, location, notes, err := validateContactBody(body, nil)
	if err != nil {
		writeFromError(w, err)
		return
	}
	id := newID("contact")
	etag := newID("etag")
	err = s.store.DB.QueryRow(r.Context(), `
		INSERT INTO contacts (id, tenant_id, user_id, email, name, company, title, phone, location, notes,
		                      source, uid, resource_name, etag)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'manual', $1, $1 || '.vcf', $11)
		ON CONFLICT (user_id, email) DO UPDATE
		SET name = EXCLUDED.name, company = EXCLUDED.company, title = EXCLUDED.title,
		    phone = EXCLUDED.phone, location = EXCLUDED.location, notes = EXCLUDED.notes,
		    etag = EXCLUDED.etag, updated_at = now()
		RETURNING id
	`, id, sess.User.TenantID, sess.User.ID, email, name, company, title, phone, location, notes, etag).Scan(&id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CONTACT", "Could not save the contact")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "contact.created", firstNonEmpty(name, email), "Contact saved")
	contact, _ := s.loadContact(r.Context(), sess.User.ID, id)
	writeJSON(w, http.StatusCreated, map[string]any{"contact": contact})
}

func (s *Server) updateContact(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	existing, err := s.loadContact(r.Context(), sess.User.ID, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Contact not found")
		return
	}
	var body contactBody
	if !s.decodeBody(w, r, &body, 256*1024) {
		return
	}
	email, name, company, title, phone, location, notes, err := validateContactBody(body, existing)
	if err != nil {
		writeFromError(w, err)
		return
	}
	_, err = s.store.DB.Exec(r.Context(), `
		UPDATE contacts SET email=$2, name=$3, company=$4, title=$5, phone=$6, location=$7, notes=$8,
		       etag=$10, updated_at=now()
		WHERE id=$1 AND user_id=$9
	`, id, email, name, company, title, phone, location, notes, sess.User.ID, newID("etag"))
	if err != nil {
		writeError(w, http.StatusConflict, "CONTACT", "Could not update the contact; the email may already exist")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "contact.updated", firstNonEmpty(name, email), "Contact updated")
	contact, _ := s.loadContact(r.Context(), sess.User.ID, id)
	writeJSON(w, http.StatusOK, map[string]any{"contact": contact})
}

func (s *Server) deleteContact(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	result, err := s.store.DB.Exec(r.Context(), `DELETE FROM contacts WHERE id=$1 AND user_id=$2`, id, sess.User.ID)
	if err != nil || result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Contact not found")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "contact.deleted", id, "Contact removed")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) loadContact(ctx context.Context, userID, id string) (map[string]any, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, email::text, name, company, title, phone, location, notes, source, created_at, updated_at
		FROM contacts WHERE user_id=$1 AND id=$2
	`, userID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, fmt.Errorf("contact not found")
	}
	var contactID, email, name, company, title, phone, location, notes, source string
	var created, updated time.Time
	if err := rows.Scan(&contactID, &email, &name, &company, &title, &phone, &location, &notes, &source, &created, &updated); err != nil {
		return nil, err
	}
	return map[string]any{
		"id": contactID, "email": strings.ToLower(email), "name": firstNonEmpty(name, email),
		"company": company, "title": title, "phone": phone, "location": location, "notes": notes,
		"source": source, "initials": initialsForName(firstNonEmpty(name, email)),
		"color": colorForString(email), "createdAt": created.UTC().Format(time.RFC3339),
		"updatedAt": updated.UTC().Format(time.RFC3339),
	}, nil
}

func validateContactBody(body contactBody, existing map[string]any) (email, name, company, title, phone, location, notes string, err error) {
	email = stateString(existing, "email")
	name = stateString(existing, "name")
	company = stateString(existing, "company")
	title = stateString(existing, "title")
	phone = stateString(existing, "phone")
	location = stateString(existing, "location")
	notes = stateString(existing, "notes")
	if body.Email != nil {
		email = strings.ToLower(strings.TrimSpace(*body.Email))
	}
	if !calendarEmailPattern.MatchString(email) {
		return "", "", "", "", "", "", "", fail(http.StatusBadRequest, "CONTACT_EMAIL", "Enter a valid contact email")
	}
	if body.Name != nil {
		name = strings.TrimSpace(*body.Name)
	}
	if name == "" {
		name = email
	}
	if body.Company != nil {
		company = truncate(*body.Company, 160)
	}
	if body.Title != nil {
		title = truncate(*body.Title, 160)
	}
	if body.Phone != nil {
		phone = truncate(*body.Phone, 80)
	}
	if body.Location != nil {
		location = truncate(*body.Location, 160)
	}
	if body.Notes != nil {
		notes = truncate(*body.Notes, 4000)
	}
	return email, name, company, title, phone, location, notes, nil
}
