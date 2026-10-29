package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type taskBody struct {
	Title         *string `json:"title"`
	Notes         *string `json:"notes"`
	Due           *string `json:"due"`
	DueTime       *string `json:"dueTime"`
	Priority      *string `json:"priority"`
	Status        *string `json:"status"`
	AssigneeEmail string  `json:"assigneeEmail"`
	Assignee      *struct {
		Email string `json:"email"`
	} `json:"assignee"`
	Tags           []string `json:"tags"`
	LinkedThreadID *string  `json:"linkedThreadId"`
	LinkedSubject  *string  `json:"linkedSubject"`
}

var taskDatePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
var taskTimePattern = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

type taskRecord struct {
	ID, Title, Notes, Status, Priority, Due, DueTime, Source     string
	AssigneeEmail, AssigneeName, AssigneeInitials, AssigneeColor string
	LinkedThreadID, LinkedSubject                                *string
	Tags                                                         []string
	CompletedAt, CreatedAt, UpdatedAt                            *time.Time
}

func (s *Server) loadTasks(ctx context.Context, userID string) ([]map[string]any, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT t.id, t.title, t.notes, t.status, t.priority,
		       COALESCE(to_char(t.due_date, 'YYYY-MM-DD'), ''),
		       COALESCE(to_char(t.due_time, 'HH24:MI'), ''),
		       COALESCE(a.email::text, ''), COALESCE(a.name, ''), COALESCE(a.initials, ''), COALESCE(a.color, ''),
		       t.linked_thread_id, t.linked_subject, t.source, t.tags,
		       t.completed_at, t.created_at, t.updated_at
		FROM tasks t
		LEFT JOIN users a ON a.id = t.assignee_user_id
		WHERE t.user_id = $1 OR t.assignee_user_id = $1
		ORDER BY (t.status = 'open') DESC, t.due_date NULLS LAST, t.created_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []map[string]any{}
	for rows.Next() {
		var task taskRecord
		if err := rows.Scan(&task.ID, &task.Title, &task.Notes, &task.Status, &task.Priority,
			&task.Due, &task.DueTime, &task.AssigneeEmail, &task.AssigneeName, &task.AssigneeInitials,
			&task.AssigneeColor, &task.LinkedThreadID, &task.LinkedSubject, &task.Source, &task.Tags,
			&task.CompletedAt, &task.CreatedAt, &task.UpdatedAt); err != nil {
			continue
		}
		tasks = append(tasks, publicTask(task))
	}
	return tasks, rows.Err()
}

func publicTask(task taskRecord) map[string]any {
	var assignee any
	if task.AssigneeEmail != "" {
		assignee = map[string]any{
			"email": task.AssigneeEmail, "name": task.AssigneeName,
			"initials": task.AssigneeInitials, "color": task.AssigneeColor,
		}
	}
	tags := []any{}
	for _, tag := range task.Tags {
		tags = append(tags, tag)
	}
	var completedAt any
	if task.CompletedAt != nil {
		completedAt = task.CompletedAt.UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"id": task.ID, "title": task.Title, "notes": task.Notes, "status": task.Status,
		"priority": task.Priority, "due": task.Due, "dueTime": task.DueTime,
		"assignee": assignee, "tags": tags, "source": task.Source,
		"linkedThreadId": task.LinkedThreadID, "linkedSubject": task.LinkedSubject,
		"completedAt": completedAt, "createdAt": task.CreatedAt.UTC().Format(time.RFC3339),
		"updatedAt": task.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func (s *Server) getTasks(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	tasks, err := s.loadTasks(r.Context(), sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TASKS", "Could not load tasks")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(tasks), "tasks": tasks})
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body taskBody
	if !s.decodeBody(w, r, &body, 256*1024) {
		return
	}
	title, notes, due, dueTime, priority, status, assigneeID, tags, linkedThreadID, linkedSubject, err := s.validateTaskBody(r.Context(), sess, body, nil)
	if err != nil {
		writeFromError(w, err)
		return
	}
	id := newID("task")
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO tasks (id, tenant_id, user_id, title, notes, status, priority, due_date, due_time,
		                   assignee_user_id, linked_thread_id, linked_subject, source, tags, completed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8,'')::date, NULLIF($9,'')::time,
		        $10, $11, $12, 'manual', $13, CASE WHEN $6 = 'done' THEN now() ELSE NULL END)
	`, id, sess.User.TenantID, sess.User.ID, title, notes, status, priority, due, dueTime,
		assigneeID, linkedThreadID, linkedSubject, tags)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TASK", "Could not create the task")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "task.created", title, fmt.Sprintf("Due %s · %s priority", firstNonEmpty(due, "unscheduled"), priority))
	task, err := s.loadTask(r.Context(), sess.User.ID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TASK", "Could not load the task")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"task": task})
}

func (s *Server) updateTask(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	existing, err := s.loadTask(r.Context(), sess.User.ID, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Task not found")
		return
	}
	var body taskBody
	if !s.decodeBody(w, r, &body, 256*1024) {
		return
	}
	owner := stateString(existing, "ownerId") == sess.User.ID
	assignee := false
	if person, _ := existing["assignee"].(map[string]any); person != nil {
		assignee = strings.EqualFold(stateString(person, "email"), sess.User.Email)
	}
	if !owner && !assignee {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "You cannot change this task")
		return
	}
	title, notes, due, dueTime, priority, status, assigneeID, tags, linkedThreadID, linkedSubject, err := s.validateTaskBody(r.Context(), sess, body, existing)
	if err != nil {
		writeFromError(w, err)
		return
	}
	var completedAt any
	if status == "done" {
		completedAt = time.Now()
		if stateString(existing, "status") == "done" {
			completedAt = existing["completedAt"]
		}
	}
	_, err = s.store.DB.Exec(r.Context(), `
		UPDATE tasks
		SET title = $2, notes = $3, status = $4, priority = $5, due_date = NULLIF($6,'')::date,
		    due_time = NULLIF($7,'')::time, assignee_user_id = $8, tags = $9,
		    linked_thread_id = $10, linked_subject = $11, completed_at = $12, updated_at = now()
		WHERE id = $1
	`, id, title, notes, status, priority, due, dueTime, assigneeID, tags, linkedThreadID, linkedSubject, completedAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TASK", "Could not update the task")
		return
	}
	action := "task.updated"
	switch {
	case status == "done" && stateString(existing, "status") != "done":
		action = "task.completed"
	case status == "open" && stateString(existing, "status") == "done":
		action = "task.reopened"
	}
	s.recordWorkspaceAudit(r.Context(), sess, action, title, "Task updated")
	task, err := s.loadTask(r.Context(), sess.User.ID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TASK", "Could not load the task")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": task})
}

func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	result, err := s.store.DB.Exec(r.Context(), `DELETE FROM tasks WHERE id = $1 AND user_id = $2`, id, sess.User.ID)
	if err != nil || result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Task not found")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "task.deleted", id, "Task removed")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) loadTask(ctx context.Context, userID, id string) (map[string]any, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT t.id, t.title, t.notes, t.status, t.priority,
		       COALESCE(to_char(t.due_date, 'YYYY-MM-DD'), ''), COALESCE(to_char(t.due_time, 'HH24:MI'), ''),
		       COALESCE(a.email::text, ''), COALESCE(a.name, ''), COALESCE(a.initials, ''), COALESCE(a.color, ''),
		       t.linked_thread_id, t.linked_subject, t.source, t.tags, t.completed_at, t.created_at, t.updated_at,
		       t.user_id::text
		FROM tasks t LEFT JOIN users a ON a.id = t.assignee_user_id
		WHERE t.id = $1 AND (t.user_id = $2 OR t.assignee_user_id = $2)
	`, id, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, fmt.Errorf("task not found")
	}
	var task taskRecord
	var ownerID string
	if err := rows.Scan(&task.ID, &task.Title, &task.Notes, &task.Status, &task.Priority, &task.Due, &task.DueTime,
		&task.AssigneeEmail, &task.AssigneeName, &task.AssigneeInitials, &task.AssigneeColor,
		&task.LinkedThreadID, &task.LinkedSubject, &task.Source, &task.Tags, &task.CompletedAt,
		&task.CreatedAt, &task.UpdatedAt, &ownerID); err != nil {
		return nil, err
	}
	result := publicTask(task)
	result["ownerId"] = ownerID
	return result, nil
}

func (s *Server) validateTaskBody(ctx context.Context, sess *sessionRecord, body taskBody, existing map[string]any) (title, notes, due, dueTime, priority, status, assigneeID string, tags []string, linkedThreadID, linkedSubject *string, err error) {
	title = stateString(existing, "title")
	notes = stateString(existing, "notes")
	due = stateString(existing, "due")
	dueTime = stateString(existing, "dueTime")
	priority = firstNonEmpty(stateString(existing, "priority"), "normal")
	status = firstNonEmpty(stateString(existing, "status"), "open")
	if person, _ := existing["assignee"].(map[string]any); person != nil {
		assigneeID = stateString(person, "email")
	} else {
		assigneeID = sess.User.Email
	}
	if existing != nil {
		if raw, ok := existing["tags"].([]any); ok {
			for _, tag := range raw {
				tags = append(tags, fmt.Sprint(tag))
			}
		}
		linkedThreadID, _ = existing["linkedThreadId"].(*string)
		linkedSubject, _ = existing["linkedSubject"].(*string)
	}
	if body.Title != nil {
		title = strings.TrimSpace(*body.Title)
	}
	if title == "" || len([]rune(title)) > 200 {
		return "", "", "", "", "", "", "", nil, nil, nil, fail(http.StatusBadRequest, "TASK_TITLE", "Task title must be between 1 and 200 characters")
	}
	if body.Notes != nil {
		notes = truncate(*body.Notes, 5000)
	}
	if body.Due != nil {
		due = strings.TrimSpace(*body.Due)
		if due != "" && !taskDatePattern.MatchString(due) {
			return "", "", "", "", "", "", "", nil, nil, nil, fail(http.StatusBadRequest, "TASK_DUE", "Due date must be YYYY-MM-DD")
		}
	}
	if body.DueTime != nil {
		dueTime = strings.TrimSpace(*body.DueTime)
		if dueTime != "" && !taskTimePattern.MatchString(dueTime) {
			return "", "", "", "", "", "", "", nil, nil, nil, fail(http.StatusBadRequest, "TASK_DUE", "Due time must be HH:MM")
		}
	}
	if body.Priority != nil {
		priority = *body.Priority
	}
	if priority != "low" && priority != "normal" && priority != "high" && priority != "urgent" {
		return "", "", "", "", "", "", "", nil, nil, nil, fail(http.StatusBadRequest, "TASK_PRIORITY", "Choose a valid priority")
	}
	if body.Status != nil {
		status = *body.Status
	}
	if status != "open" && status != "done" {
		return "", "", "", "", "", "", "", nil, nil, nil, fail(http.StatusBadRequest, "TASK_STATUS", "Choose a valid task status")
	}
	email := body.AssigneeEmail
	if email == "" && body.Assignee != nil {
		email = body.Assignee.Email
	}
	if email != "" {
		assigneeID = strings.ToLower(strings.TrimSpace(email))
	}
	assignee, lookupErr := s.workspaceUserByEmail(ctx, sess.User.TenantID, assigneeID)
	if lookupErr != nil {
		return "", "", "", "", "", "", "", nil, nil, nil, fail(http.StatusBadRequest, "TASK_ASSIGNEE", "Choose a person from the Meridian directory")
	}
	if !strings.EqualFold(assignee.Email, sess.User.Email) {
		policy := s.tenantPolicyFor(ctx, sess.User.TenantID)
		if !policy.Directory.ContactSharing && !webmailAdminRole(sess.User.Role) {
			return "", "", "", "", "", "", "", nil, nil, nil, fail(http.StatusForbidden, "DIRECTORY_HIDDEN", "Your organization does not allow directory-based assignment")
		}
	}
	assigneeID = assignee.ID
	if body.Tags != nil {
		if len(body.Tags) > 10 {
			return "", "", "", "", "", "", "", nil, nil, nil, fail(http.StatusBadRequest, "TASK_TAGS", "Use up to ten tags")
		}
		tags = []string{}
		for _, tag := range body.Tags {
			clean := truncate(strings.TrimSpace(tag), 24)
			if clean != "" {
				tags = append(tags, clean)
			}
		}
	}
	if body.LinkedThreadID != nil {
		linkedThreadID = optionalText(*body.LinkedThreadID, 80)
	}
	if body.LinkedSubject != nil {
		linkedSubject = optionalText(*body.LinkedSubject, 240)
	}
	if tags == nil {
		tags = []string{}
	}
	return title, notes, due, dueTime, priority, status, assigneeID, tags, linkedThreadID, linkedSubject, nil
}

func optionalText(value string, limit int) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	value = truncate(value, limit)
	return &value
}

func (s *Server) workspaceUserByEmail(ctx context.Context, tenantID, email string) (userRecord, error) {
	var user userRecord
	err := s.store.DB.QueryRow(ctx, `
		SELECT id::text, tenant_id::text, email::text, name, initials, color, role, status
		FROM users
		WHERE tenant_id = $1 AND lower(email::text) = lower($2) AND status = 'active' AND deleted_at IS NULL
	`, tenantID, strings.TrimSpace(email)).Scan(&user.ID, &user.TenantID, &user.Email, &user.Name, &user.Initials, &user.Color, &user.Role, &user.Status)
	if err != nil {
		return userRecord{}, err
	}
	return user, nil
}
