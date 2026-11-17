package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"
)

func (s *Server) ensureDefaultAddressBook(ctx context.Context, user userRecord) error {
	_, err := s.store.DB.Exec(ctx, `
		INSERT INTO address_books (id, tenant_id, user_id, name, color, is_default)
		VALUES ('default', $1, $2, 'Contacts', '#3d6b62', true)
		ON CONFLICT (user_id, id) DO NOTHING
	`, user.TenantID, user.ID)
	return err
}

func (s *Server) loadAddressBooks(ctx context.Context, user userRecord) ([]map[string]any, error) {
	_ = s.ensureDefaultAddressBook(ctx, user)
	rows, err := s.store.DB.Query(ctx, `
		SELECT b.id, b.name, b.color, b.is_default, b.source, b.created_at,
		       (SELECT count(*) FROM contacts c WHERE c.user_id=b.user_id AND c.address_book_id=b.id)
		FROM address_books b WHERE b.user_id=$1
		ORDER BY b.is_default DESC, lower(b.name)
	`, user.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	books := []map[string]any{}
	for rows.Next() {
		var id, name, color, source string
		var isDefault bool
		var createdAt time.Time
		var count int64
		if rows.Scan(&id, &name, &color, &isDefault, &source, &createdAt, &count) == nil {
			books = append(books, map[string]any{
				"id": id, "name": name, "color": color, "isDefault": isDefault,
				"source": source, "count": count, "createdAt": createdAt.UTC().Format(time.RFC3339),
			})
		}
	}
	return books, rows.Err()
}

func (s *Server) getAddressBooks(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	books, err := s.loadAddressBooks(r.Context(), sess.User)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ADDRESS_BOOKS", "Could not load address books")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"addressBooks": books, "count": len(books)})
}

type addressBookBody struct {
	Name  *string `json:"name"`
	Color *string `json:"color"`
}

func (s *Server) createAddressBook(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body addressBookBody
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	name := "New address book"
	if body.Name != nil {
		name = strings.TrimSpace(*body.Name)
	}
	if name == "" || len([]rune(name)) > 80 {
		writeError(w, http.StatusBadRequest, "ADDRESS_BOOK_NAME", "Give the address book a name of 1–80 characters")
		return
	}
	color := "#3d6b62"
	if body.Color != nil && strings.TrimSpace(*body.Color) != "" {
		if !calendarHexPattern.MatchString(strings.TrimSpace(*body.Color)) {
			writeError(w, http.StatusBadRequest, "ADDRESS_BOOK_COLOR", "Choose a six-digit hex colour")
			return
		}
		color = strings.ToLower(strings.TrimSpace(*body.Color))
	}
	var count int
	_ = s.store.DB.QueryRow(r.Context(), `SELECT count(*) FROM address_books WHERE user_id=$1`, sess.User.ID).Scan(&count)
	if count >= 20 {
		writeError(w, http.StatusConflict, "ADDRESS_BOOK_LIMIT", "You can keep up to 20 address books")
		return
	}
	id := newID("book")
	if _, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO address_books (id, tenant_id, user_id, name, color)
		VALUES ($1,$2,$3,$4,$5)
	`, id, sess.User.TenantID, sess.User.ID, name, color); err != nil {
		writeError(w, http.StatusInternalServerError, "ADDRESS_BOOK", "Could not create the address book")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "contact.book_created", name, "Created an address book")
	books, _ := s.loadAddressBooks(r.Context(), sess.User)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "addressBooks": books, "id": id})
}

func (s *Server) updateAddressBook(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	var body addressBookBody
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	var name, color string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT name, color FROM address_books WHERE user_id=$1 AND id=$2
	`, sess.User.ID, id).Scan(&name, &color); err != nil {
		writeError(w, http.StatusNotFound, "ADDRESS_BOOK_NOT_FOUND", "Address book not found")
		return
	}
	if body.Name != nil {
		value := strings.TrimSpace(*body.Name)
		if value == "" || len([]rune(value)) > 80 {
			writeError(w, http.StatusBadRequest, "ADDRESS_BOOK_NAME", "Give the address book a name of 1–80 characters")
			return
		}
		name = value
	}
	if body.Color != nil && strings.TrimSpace(*body.Color) != "" {
		if !calendarHexPattern.MatchString(strings.TrimSpace(*body.Color)) {
			writeError(w, http.StatusBadRequest, "ADDRESS_BOOK_COLOR", "Choose a six-digit hex colour")
			return
		}
		color = strings.ToLower(strings.TrimSpace(*body.Color))
	}
	if _, err := s.store.DB.Exec(r.Context(), `
		UPDATE address_books SET name=$3, color=$4, updated_at=now() WHERE user_id=$1 AND id=$2
	`, sess.User.ID, id, name, color); err != nil {
		writeError(w, http.StatusInternalServerError, "ADDRESS_BOOK", "Could not update the address book")
		return
	}
	books, _ := s.loadAddressBooks(r.Context(), sess.User)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "addressBooks": books})
}

func (s *Server) deleteAddressBook(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	if id == "default" {
		writeError(w, http.StatusBadRequest, "ADDRESS_BOOK_DEFAULT", "The default address book cannot be deleted")
		return
	}
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ADDRESS_BOOK", "Could not delete the address book")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `
		UPDATE contacts SET address_book_id='default' WHERE user_id=$1 AND address_book_id=$2
	`, sess.User.ID, id); err != nil {
		writeError(w, http.StatusInternalServerError, "ADDRESS_BOOK", "Could not move the contacts")
		return
	}
	result, err := tx.Exec(r.Context(), `DELETE FROM address_books WHERE user_id=$1 AND id=$2`, sess.User.ID, id)
	if err != nil || result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "ADDRESS_BOOK_NOT_FOUND", "Address book not found")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "ADDRESS_BOOK", "Could not commit the address book deletion")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "contact.book_deleted", id, "Deleted an address book; contacts moved to the default book")
	books, _ := s.loadAddressBooks(r.Context(), sess.User)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "addressBooks": books})
}
