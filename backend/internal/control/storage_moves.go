package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type storageOperation struct {
	ID              string
	Kind            string
	Status          string
	Plan            []byte
	PlanHash        string
	PlanExpiresAt   *time.Time
	Step            string
	Progress        int
	TotalBytes      int64
	MovedBytes      int64
	Log             string
	Error           string
	CancelRequested bool
	ActorID         string
	ActorEmail      string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	StartedAt       *time.Time
	FinishedAt      *time.Time
	FinalizeAt      *time.Time
}

type storageMoveItem struct {
	ID               string
	OperationID      string
	MailboxID        string
	Email            string
	SourceHome       string
	TargetHome       string
	SourceMailHome   string
	SourceVolumeID   string
	SourceVolumeRoot string
	SourceVolumeName string
	TargetVolumeID   string
	TargetVolumeRoot string
	TargetVolumeName string
	TargetLegacy     bool
	Status           string
	Bytes            int64
	Messages         int64
	Error            string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	StartedAt        *time.Time
	FinishedAt       *time.Time
}

type storageMovePlan struct {
	Scope                  string                `json:"scope"`
	Domain                 string                `json:"domain,omitempty"`
	TargetVolumeID         string                `json:"targetVolumeId"`
	TargetVolumeName       string                `json:"targetVolumeName"`
	TargetMountPath        string                `json:"targetMountPath"`
	TargetLegacy           bool                  `json:"targetLegacy"`
	Verify                 string                `json:"verify"`
	GraceHours             int                   `json:"graceHours"`
	PreviousDomainVolumeID string                `json:"previousDomainVolumeId,omitempty"`
	Items                  []storageMovePlanItem `json:"items"`
}

type storageMovePlanItem struct {
	MailboxID        string `json:"mailboxId"`
	Email            string `json:"email"`
	SourceHome       string `json:"sourceHome"`
	SourceMailHome   string `json:"sourceMailHome"`
	SourceVolumeID   string `json:"sourceVolumeId"`
	SourceVolumeRoot string `json:"sourceVolumeRoot"`
	SourceVolumeName string `json:"sourceVolumeName"`
	TargetHome       string `json:"targetHome"`
	TargetVolumeID   string `json:"targetVolumeId"`
	TargetVolumeRoot string `json:"targetVolumeRoot"`
	TargetVolumeName string `json:"targetVolumeName"`
	TargetLegacy     bool   `json:"targetLegacy"`
	Bytes            int64  `json:"bytes"`
	Messages         int64  `json:"messages"`
}

func (s *Server) registerStorageMoveRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /control/api/storage/moves/plan", s.require("storage.manage", s.handleStorageMovePlan))
	mux.HandleFunc("POST /control/api/storage/moves/execute", s.require("storage.manage", s.handleStorageMoveExecute))
	mux.HandleFunc("GET /control/api/storage/mailboxes", s.require("storage.read", s.handleStorageMailboxes))
	mux.HandleFunc("GET /control/api/storage/operations", s.require("storage.read", s.handleStorageOperations))
	mux.HandleFunc("GET /control/api/storage/operations/{id}", s.require("storage.read", s.handleStorageOperation))
	mux.HandleFunc("POST /control/api/storage/operations/{id}/cancel", s.require("storage.manage", s.handleStorageOperationCancel))
	mux.HandleFunc("POST /control/api/storage/operations/{id}/rollback", s.require("storage.manage", s.handleStorageOperationRollback))
	mux.HandleFunc("POST /control/api/storage/operations/{id}/finalize", s.require("storage.manage", s.handleStorageOperationFinalize))
}

func (s *Server) loadStorageOperation(ctx context.Context, id string) (storageOperation, error) {
	var op storageOperation
	err := s.db.QueryRow(ctx, `
		SELECT id::text, kind, status, plan, plan_hash, plan_expires_at, step, progress,
		       total_bytes, moved_bytes, log, error, cancel_requested,
		       COALESCE(actor_id::text, ''), actor_email, created_at, updated_at,
		       started_at, finished_at, finalize_at
		FROM control.storage_operations WHERE id = $1
	`, strings.TrimSpace(id)).Scan(
		&op.ID, &op.Kind, &op.Status, &op.Plan, &op.PlanHash, &op.PlanExpiresAt,
		&op.Step, &op.Progress, &op.TotalBytes, &op.MovedBytes, &op.Log, &op.Error,
		&op.CancelRequested, &op.ActorID, &op.ActorEmail, &op.CreatedAt, &op.UpdatedAt,
		&op.StartedAt, &op.FinishedAt, &op.FinalizeAt,
	)
	return op, err
}

func (s *Server) loadStorageMoveItems(ctx context.Context, operationID string) ([]storageMoveItem, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id::text, operation_id::text, mailbox_id::text, email::text,
		       source_home, target_home, source_mail_home, COALESCE(source_volume_id::text, ''),
		       source_volume_root, source_volume_name, target_volume_id::text,
		       target_volume_root, target_volume_name, target_is_legacy,
		       status, bytes, messages, error, created_at, updated_at, started_at, finished_at
		FROM control.storage_move_items
		WHERE operation_id = $1
		ORDER BY email
	`, operationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []storageMoveItem{}
	for rows.Next() {
		var item storageMoveItem
		if err := rows.Scan(
			&item.ID, &item.OperationID, &item.MailboxID, &item.Email, &item.SourceHome,
			&item.TargetHome, &item.SourceMailHome, &item.SourceVolumeID,
			&item.SourceVolumeRoot, &item.SourceVolumeName, &item.TargetVolumeID,
			&item.TargetVolumeRoot, &item.TargetVolumeName, &item.TargetLegacy,
			&item.Status, &item.Bytes, &item.Messages, &item.Error, &item.CreatedAt,
			&item.UpdatedAt, &item.StartedAt, &item.FinishedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Server) handleStorageMovePlan(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Scope          string   `json:"scope"`
		MailboxIDs     []string `json:"mailboxIds"`
		DomainID       string   `json:"domainId"`
		Domain         string   `json:"domain"`
		TargetVolumeID string   `json:"targetVolumeId"`
		Verify         string   `json:"verify"`
		GraceHours     int      `json:"graceHours"`
	}
	if err := decodeJSON(w, r, &body, 1<<20); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if !s.storageDoctor(r.Context()).UserDBReady {
		writeError(w, http.StatusConflict, "STORAGE_DOCTOR", "Dovecot does not read per-mailbox homes yet. Apply the managed userdb query before moving mail.")
		return
	}
	target, err := s.validateStorageVolumeForPlacement(r.Context(), strings.TrimSpace(body.TargetVolumeID))
	if err != nil {
		writeFromError(w, err)
		return
	}
	scope := strings.ToLower(strings.TrimSpace(body.Scope))
	if scope == "" {
		if strings.TrimSpace(body.DomainID) != "" || strings.TrimSpace(body.Domain) != "" {
			scope = "domain"
		} else {
			scope = "mailbox"
		}
	}
	ids := uniqueNonEmpty(body.MailboxIDs)
	domainFilter := strings.TrimSpace(body.Domain)
	if scope == "domain" {
		if strings.TrimSpace(body.DomainID) != "" {
			if err := s.db.QueryRow(r.Context(), `SELECT domain::text FROM public.mail_domains WHERE id = $1`, body.DomainID).Scan(&domainFilter); err != nil {
				writeFromError(w, err)
				return
			}
		}
		if normalizeDomain(domainFilter) == "" {
			writeError(w, http.StatusBadRequest, "DOMAIN", "Choose a domain to move")
			return
		}
		domainFilter = normalizeDomain(domainFilter)
	} else {
		if len(ids) == 0 {
			writeError(w, http.StatusBadRequest, "MAILBOX", "Choose at least one mailbox to move")
			return
		}
		if len(ids) > 500 {
			writeError(w, http.StatusBadRequest, "MAILBOX", "Plan at most 500 mailboxes per operation")
			return
		}
	}
	query := `
		SELECT m.id::text, m.email::text, m.domain::text, m.local_part::text,
		       COALESCE(m.mail_home, ''), COALESCE(m.storage_volume_id::text, ''),
		       COALESCE(v.mount_path, ''), COALESCE(v.name, ''), m.storage_state
		FROM public.mailboxes m
		LEFT JOIN control.storage_volumes v ON v.id = m.storage_volume_id
		WHERE m.active = true`
	args := []any{}
	if scope == "domain" {
		query += ` AND m.domain = $1`
		args = append(args, domainFilter)
	} else {
		query += ` AND m.id::text = ANY($1::text[])`
		args = append(args, ids)
	}
	query += ` ORDER BY m.email`
	rows, err := s.db.Query(r.Context(), query, args...)
	if err != nil {
		writeFromError(w, err)
		return
	}
	type candidate struct {
		id, email, domain, local, mailHome, sourceVolumeID, sourceRoot, sourceName, state string
	}
	candidates := []candidate{}
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.email, &item.domain, &item.local,
			&item.mailHome, &item.sourceVolumeID, &item.sourceRoot, &item.sourceName, &item.state); err != nil {
			rows.Close()
			writeFromError(w, err)
			return
		}
		candidates = append(candidates, item)
	}
	rows.Close()
	if len(candidates) == 0 {
		writeError(w, http.StatusNotFound, "MAILBOX", "No active mailboxes matched that selection")
		return
	}
	policy, err := s.storagePolicyRow(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	graceHours := body.GraceHours
	if graceHours <= 0 {
		graceHours = policy.MigrationGraceHours
	}
	if graceHours < 1 {
		graceHours = 1
	}
	if graceHours > 336 {
		graceHours = 336
	}
	verify := strings.ToLower(strings.TrimSpace(body.Verify))
	if verify == "" {
		verify = "standard"
	}
	if verify != "standard" && verify != "thorough" {
		writeError(w, http.StatusBadRequest, "VERIFY", "Verification must be standard or thorough")
		return
	}
	targetStat, statErr := filesystemStatLookup(target.MountPath)
	if statErr != nil {
		writeError(w, http.StatusConflict, "STORAGE_VOLUME", "The target volume is not readable: "+statErr.Error())
		return
	}
	plan := storageMovePlan{
		Scope: scope, Domain: domainFilter, TargetVolumeID: target.ID,
		TargetVolumeName: target.Name, TargetMountPath: target.MountPath,
		TargetLegacy: sameStoragePath(target.MountPath, s.cfg.VmailRoot),
		Verify:       verify, GraceHours: graceHours, Items: []storageMovePlanItem{},
	}
	var totalBytes int64
	for _, item := range candidates {
		sourceRoot := strings.TrimSpace(item.sourceRoot)
		if sourceRoot == "" {
			sourceRoot = s.cfg.VmailRoot
		}
		sourceRoot = filepath.Clean(sourceRoot)
		sourceHome := strings.TrimSpace(item.mailHome)
		if sourceHome == "" {
			sourceHome = filepath.Join(s.cfg.VmailRoot, item.domain, item.local)
		}
		sourceHome = filepath.Clean(sourceHome)
		if !pathWithin(sourceRoot, sourceHome) || sameStoragePath(sourceRoot, sourceHome) {
			writeError(w, http.StatusConflict, "MAILDIR", "A source mailbox home is outside its registered volume: "+item.email)
			return
		}
		targetHome := filepath.Join(target.MountPath, item.domain, item.local)
		if sameStoragePath(sourceHome, targetHome) {
			writeError(w, http.StatusConflict, "STORAGE_VOLUME", item.email+" is already on that volume")
			return
		}
		if info, err := os.Lstat(sourceHome); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			writeError(w, http.StatusConflict, "MAILDIR", "The source home is not a readable directory: "+item.email)
			return
		}
		if _, err := os.Lstat(targetHome); err == nil {
			writeError(w, http.StatusConflict, "MAILDIR", "The target home already exists: "+item.email)
			return
		}
		if item.state == "moving" || item.state == "switching" {
			writeError(w, http.StatusConflict, "STORAGE_BUSY", item.email+" already has a storage operation in progress")
			return
		}
		var active int
		_ = s.db.QueryRow(r.Context(), `
			SELECT count(*) FROM control.storage_move_items i
			JOIN control.storage_operations o ON o.id = i.operation_id
			WHERE i.mailbox_id = $1::uuid AND o.status IN ('queued', 'running')
		`, item.id).Scan(&active)
		if active > 0 {
			writeError(w, http.StatusConflict, "STORAGE_BUSY", item.email+" already has a queued or running move")
			return
		}
		stats, err := mailHomeStats(sourceHome)
		if err != nil {
			writeError(w, http.StatusConflict, "MAILDIR", "Could not measure the source mailbox: "+err.Error())
			return
		}
		totalBytes += stats.Bytes
		sourceName := strings.TrimSpace(item.sourceName)
		if sourceName == "" {
			sourceName = "legacy"
		}
		plan.Items = append(plan.Items, storageMovePlanItem{
			MailboxID: item.id, Email: item.email, SourceHome: sourceHome,
			SourceMailHome: item.mailHome, SourceVolumeID: item.sourceVolumeID,
			SourceVolumeRoot: sourceRoot, SourceVolumeName: sourceName,
			TargetHome: targetHome, TargetVolumeID: target.ID,
			TargetVolumeRoot: target.MountPath, TargetVolumeName: target.Name,
			TargetLegacy: plan.TargetLegacy, Bytes: stats.Bytes, Messages: stats.Messages,
		})
	}
	required := uint64(totalBytes) + uint64(totalBytes)/10 + uint64(policy.MinFreeBytes)
	if targetStat.FreeBytes < required {
		writeError(w, http.StatusConflict, "STORAGE_SPACE", fmt.Sprintf(
			"Not enough free space on %s: %s free, %s required including the reserve.",
			target.Name, humanBytes(targetStat.FreeBytes), humanBytes(required)))
		return
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		writeFromError(w, err)
		return
	}
	planHash := canonicalPlanHash(planJSON)
	expiresAt := time.Now().UTC().Add(10 * time.Minute)
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var operationID string
	err = tx.QueryRow(r.Context(), `
		INSERT INTO control.storage_operations
		  (kind, status, plan, plan_hash, plan_expires_at, step, total_bytes, actor_id, actor_email)
		VALUES ('move_mailboxes', 'planned', $1::jsonb, $2, $3, 'Ready to execute', $4,
		        NULLIF($5, '')::uuid, $6)
		RETURNING id::text
	`, string(planJSON), planHash, expiresAt, totalBytes, sess.User.ID, sess.User.Email).Scan(&operationID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	for _, item := range plan.Items {
		_, err := tx.Exec(r.Context(), `
			INSERT INTO control.storage_move_items
			  (operation_id, mailbox_id, email, source_home, target_home, source_mail_home,
			   source_volume_id, source_volume_root, source_volume_name, target_volume_id,
			   target_volume_root, target_volume_name, target_is_legacy, bytes, messages)
			VALUES ($1, $2::uuid, $3, $4, $5, $6, NULLIF($7, '')::uuid, $8, $9,
			        $10::uuid, $11, $12, $13, $14, $15)
		`, operationID, item.MailboxID, item.Email, item.SourceHome, item.TargetHome,
			item.SourceMailHome, item.SourceVolumeID, item.SourceVolumeRoot, item.SourceVolumeName,
			item.TargetVolumeID, item.TargetVolumeRoot, item.TargetVolumeName, item.TargetLegacy,
			item.Bytes, item.Messages)
		if err != nil {
			writeFromError(w, err)
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeFromError(w, err)
		return
	}
	token := s.signCSRF("storage-move:" + operationID + ":" + planHash)
	s.actorAudit(r, sess, "storage.move_planned", operationID, "Planned a mailbox move", map[string]any{
		"scope": scope, "mailboxes": len(plan.Items), "bytes": totalBytes,
		"targetVolumeId": target.ID, "targetVolume": target.Name,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "operationId": operationID, "token": token,
		"expiresAt": expiresAt.Format(time.RFC3339), "plan": plan,
		"summary": map[string]any{
			"mailboxes": len(plan.Items), "bytes": totalBytes,
			"targetVolume": target.Name, "targetMountPath": target.MountPath,
			"freeAfter":  int64(targetStat.FreeBytes) - totalBytes,
			"graceHours": graceHours, "verify": verify,
		},
	})
}

func (s *Server) handleStorageMoveExecute(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		OperationID string `json:"operationId"`
		Token       string `json:"token"`
		Confirm     bool   `json:"confirm"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if !body.Confirm {
		writeError(w, http.StatusBadRequest, "CONFIRM", "Confirm the move plan before executing it")
		return
	}
	op, err := s.loadStorageOperation(r.Context(), body.OperationID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if op.Status != "planned" {
		writeError(w, http.StatusConflict, "OPERATION", "That operation is no longer waiting to execute")
		return
	}
	if op.PlanExpiresAt != nil && time.Now().After(*op.PlanExpiresAt) {
		writeError(w, http.StatusConflict, "EXPIRED", "This plan has expired. Create a new plan so the source and target can be rechecked.")
		return
	}
	if !constantTimeEqual(op.PlanHash, canonicalPlanHash(op.Plan)) || !constantTimeEqual(body.Token, s.signCSRF("storage-move:"+op.ID+":"+op.PlanHash)) {
		writeError(w, http.StatusForbidden, "CONFIRM", "The plan token is invalid. Create a new plan.")
		return
	}
	if !s.storageDoctor(r.Context()).UserDBReady {
		writeError(w, http.StatusConflict, "STORAGE_DOCTOR", "Dovecot does not read per-mailbox homes yet")
		return
	}
	items, err := s.loadStorageMoveItems(r.Context(), op.ID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if len(items) == 0 {
		writeError(w, http.StatusConflict, "OPERATION", "The operation has no mailbox items")
		return
	}
	var totalBytes int64
	for _, item := range items {
		var mailHome, volumeID, state string
		err := s.db.QueryRow(r.Context(), `
			SELECT COALESCE(mail_home, ''), COALESCE(storage_volume_id::text, ''), storage_state
			FROM public.mailboxes WHERE id = $1
		`, item.MailboxID).Scan(&mailHome, &volumeID, &state)
		if err != nil {
			writeFromError(w, err)
			return
		}
		if strings.TrimSpace(mailHome) != strings.TrimSpace(item.SourceMailHome) || strings.TrimSpace(volumeID) != strings.TrimSpace(item.SourceVolumeID) || state == "moving" || state == "switching" {
			writeError(w, http.StatusConflict, "OPERATION", item.Email+" changed after the plan was created. Create a new plan.")
			return
		}
		totalBytes += item.Bytes
	}
	var plan storageMovePlan
	if json.Unmarshal(op.Plan, &plan) == nil && plan.TargetVolumeID != "" {
		target, targetErr := s.validateStorageVolumeForPlacement(r.Context(), plan.TargetVolumeID)
		if targetErr != nil {
			writeFromError(w, targetErr)
			return
		}
		stat, statErr := filesystemStatLookup(target.MountPath)
		if statErr != nil {
			writeError(w, http.StatusConflict, "STORAGE_VOLUME", "The target volume is no longer readable: "+statErr.Error())
			return
		}
		policy, _ := s.storagePolicyRow(r.Context())
		required := uint64(totalBytes) + uint64(totalBytes)/10 + uint64(policy.MinFreeBytes)
		if stat.FreeBytes < required {
			writeError(w, http.StatusConflict, "STORAGE_SPACE", "The target no longer has enough free space for this move")
			return
		}
	}
	if _, err := s.db.Exec(r.Context(), `
		UPDATE control.storage_operations
		SET status = 'queued', step = 'Queued', error = '', updated_at = now()
		WHERE id = $1 AND status = 'planned'
	`, op.ID); err != nil {
		writeFromError(w, err)
		return
	}
	if err := s.enqueueStorageOperation(op.ID); err != nil {
		_, _ = s.db.Exec(context.Background(), `
			UPDATE control.storage_operations SET status = 'failed', error = $2,
			       step = 'Could not start the storage runner', finished_at = now(), updated_at = now()
			WHERE id = $1
		`, op.ID, truncateText(err.Error(), 500))
		writeError(w, http.StatusBadGateway, "RUNNER", "Could not start the storage runner: "+err.Error())
		return
	}
	s.actorAudit(r, sess, "storage.move_started", op.ID, "Started a verified mailbox move", map[string]any{"mailboxes": len(items)})
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "operationId": op.ID})
}

func (s *Server) handleStorageMailboxes(w http.ResponseWriter, r *http.Request, _ *sessionRecord) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	volume := strings.TrimSpace(r.URL.Query().Get("volume"))
	idsOnly := queryBool(r, "idsOnly", false)
	limit := queryInt(r, "limit", 25, 1, 100)
	if idsOnly {
		// Bulk selection is intentionally bounded so a move plan stays
		// reviewable. Callers narrow the search rather than selecting an
		// unbounded tenant in one click.
		limit = queryInt(r, "limit", 10000, 1, 10000)
	}
	page := queryInt(r, "page", 1, 1, 1000000)
	offset := (page - 1) * limit
	volumeFilter := "($2 = '' OR ($2 = 'legacy' AND m.storage_volume_id IS NULL) OR m.storage_volume_id::text = $2)"
	var total int
	if err := s.db.QueryRow(r.Context(), `
		SELECT count(*)
		FROM public.mailboxes m
		JOIN public.users u ON u.id = m.user_id
		WHERE m.active = true
		  AND ($1 = '' OR m.email::text ILIKE '%' || $1 || '%' OR u.name ILIKE '%' || $1 || '%')
		  AND `+volumeFilter+`
	`, query, volume).Scan(&total); err != nil {
		writeFromError(w, err)
		return
	}
	if idsOnly {
		rows, err := s.db.Query(r.Context(), `
			SELECT m.id::text
			FROM public.mailboxes m
			JOIN public.users u ON u.id = m.user_id
			WHERE m.active = true
			  AND ($1 = '' OR m.email::text ILIKE '%' || $1 || '%' OR u.name ILIKE '%' || $1 || '%')
			  AND `+volumeFilter+`
			ORDER BY m.email
			LIMIT $3 OFFSET $4
		`, query, volume, limit, offset)
		if err != nil {
			writeFromError(w, err)
			return
		}
		defer rows.Close()
		ids := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				writeFromError(w, err)
				return
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			writeFromError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ids": ids, "total": total, "page": page, "limit": limit,
			"truncated": total > len(ids),
		})
		return
	}
	rows, err := s.db.Query(r.Context(), `
		SELECT m.id::text, m.email::text, m.domain::text,
		       COALESCE(v.name, ''), COALESCE(m.mail_home, ''), m.storage_state,
		       COALESCE(u.name, ''), u.quota_bytes, u.used_bytes,
		       COALESCE(m.provision_status, '')
		FROM public.mailboxes m
		JOIN public.users u ON u.id = m.user_id
		LEFT JOIN control.storage_volumes v ON v.id = m.storage_volume_id
		WHERE m.active = true
		  AND ($1 = '' OR m.email::text ILIKE '%' || $1 || '%' OR u.name ILIKE '%' || $1 || '%')
		  AND `+volumeFilter+`
		ORDER BY m.email
		LIMIT $3 OFFSET $4
	`, query, volume, limit, offset)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	mailboxes := []map[string]any{}
	for rows.Next() {
		var id, email, domain, volumeName, home, state, userName, provisionStatus string
		var quotaBytes, usedBytes int64
		if err := rows.Scan(&id, &email, &domain, &volumeName, &home, &state,
			&userName, &quotaBytes, &usedBytes, &provisionStatus); err != nil {
			writeFromError(w, err)
			return
		}
		mailboxes = append(mailboxes, map[string]any{
			"id": id, "email": email, "domain": domain, "volume": volumeName,
			"mailHome": home, "state": state, "userName": userName,
			"quotaBytes": quotaBytes, "usedBytes": usedBytes,
			"provisionStatus": provisionStatus,
		})
	}
	if err := rows.Err(); err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mailboxes": mailboxes, "total": total, "page": page, "limit": limit,
		"pages": (total + limit - 1) / limit,
	})
}

func (s *Server) handleStorageOperations(w http.ResponseWriter, r *http.Request, _ *sessionRecord) {
	limit := queryInt(r, "limit", 10, 1, 100)
	page := queryInt(r, "page", 1, 1, 1000000)
	offset := (page - 1) * limit
	sortKey := strings.TrimSpace(r.URL.Query().Get("sort"))
	sortExpressions := map[string]string{
		"operation": "kind",
		"target":    "COALESCE(NULLIF(plan->>'targetVolumeName', ''), NULLIF(plan->>'volumeName', ''), NULLIF(plan->>'name', ''), NULLIF(plan->>'mountPath', ''), '')",
		"mailboxes": "CASE WHEN kind = 'move_mailboxes' AND jsonb_typeof(plan->'items') = 'array' THEN jsonb_array_length(plan->'items') ELSE 0 END",
		"status":    "status",
		"createdAt": "created_at",
	}
	sortExpression, ok := sortExpressions[sortKey]
	if !ok {
		sortExpression = "created_at"
	}
	order := "DESC"
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("order")), "asc") {
		order = "ASC"
	}
	var total int
	if err := s.db.QueryRow(r.Context(), `
		SELECT count(*) FROM control.storage_operations
		WHERE kind = 'move_mailboxes' OR kind LIKE 'volume_%'
	`).Scan(&total); err != nil {
		writeFromError(w, err)
		return
	}
	rows, err := s.db.Query(r.Context(), `
		SELECT id::text, kind, status, plan, plan_hash, plan_expires_at, step, progress,
		       total_bytes, moved_bytes, log, error, cancel_requested,
		       COALESCE(actor_id::text, ''), actor_email, created_at, updated_at,
		       started_at, finished_at, finalize_at
		FROM control.storage_operations
		WHERE kind = 'move_mailboxes' OR kind LIKE 'volume_%'
		ORDER BY `+sortExpression+` `+order+`, created_at DESC
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	operations := []map[string]any{}
	for rows.Next() {
		var op storageOperation
		if err := rows.Scan(
			&op.ID, &op.Kind, &op.Status, &op.Plan, &op.PlanHash, &op.PlanExpiresAt,
			&op.Step, &op.Progress, &op.TotalBytes, &op.MovedBytes, &op.Log, &op.Error,
			&op.CancelRequested, &op.ActorID, &op.ActorEmail, &op.CreatedAt, &op.UpdatedAt,
			&op.StartedAt, &op.FinishedAt, &op.FinalizeAt,
		); err != nil {
			writeFromError(w, err)
			return
		}
		operations = append(operations, storageOperationSummary(op))
	}
	if err := rows.Err(); err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"operations": operations, "total": total, "page": page, "limit": limit,
		"pages": (total + limit - 1) / limit,
	})
}

func (s *Server) handleStorageOperation(w http.ResponseWriter, r *http.Request, _ *sessionRecord) {
	op, err := s.loadStorageOperation(r.Context(), pathID(r))
	if err != nil {
		writeFromError(w, err)
		return
	}
	items, err := s.loadStorageMoveItems(r.Context(), op.ID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	itemViews := make([]map[string]any, 0, len(items))
	for _, item := range items {
		itemViews = append(itemViews, storageMoveItemView(item))
	}
	view := storageOperationSummary(op)
	view["items"] = itemViews
	view["log"] = op.Log
	writeJSON(w, http.StatusOK, map[string]any{"operation": view})
}

func (s *Server) handleStorageOperationCancel(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	op, err := s.loadStorageOperation(r.Context(), pathID(r))
	if err != nil {
		writeFromError(w, err)
		return
	}
	switch op.Status {
	case "planned", "queued":
		if _, err := s.db.Exec(r.Context(), `
			UPDATE control.storage_operations SET status = 'canceled', step = 'Canceled',
			       finished_at = now(), updated_at = now()
			WHERE id = $1 AND status IN ('planned', 'queued')
		`, op.ID); err != nil {
			writeFromError(w, err)
			return
		}
		s.auditStorageOperation(r.Context(), op, "storage.move_canceled", "Canceled a mailbox move before it started", nil)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "canceled"})
	case "running":
		_, err := s.db.Exec(r.Context(), `
			UPDATE control.storage_operations SET cancel_requested = true, step = 'Cancellation requested', updated_at = now()
			WHERE id = $1 AND status = 'running'
		`, op.ID)
		if err != nil {
			writeFromError(w, err)
			return
		}
		s.auditStorageOperation(r.Context(), op, "storage.move_cancel_requested", "Requested cancellation of a running mailbox move", nil)
		writeJSON(w, http.StatusAccepted, map[string]any{
			"ok": true, "status": "running",
			"message": "Cancellation requested. The runner stops at the next safe checkpoint; a mailbox that has already switched can still be rolled back.",
		})
	default:
		writeError(w, http.StatusConflict, "OPERATION", "This operation can no longer be canceled")
	}
}

func (s *Server) handleStorageOperationRollback(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	op, err := s.loadStorageOperation(r.Context(), pathID(r))
	if err != nil {
		writeFromError(w, err)
		return
	}
	if op.Status != "grace" && op.Status != "failed" {
		writeError(w, http.StatusConflict, "OPERATION", "Only a failed or grace operation can be rolled back. Cancel a running operation first.")
		return
	}
	if err := s.RollbackStorageOperation(r.Context(), op.ID); err != nil {
		writeError(w, http.StatusBadGateway, "ROLLBACK", "Rollback failed: "+err.Error())
		return
	}
	s.actorAudit(r, sess, "storage.move_rolled_back", op.ID, "Rolled back a mailbox move", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "rolled_back"})
}

func (s *Server) handleStorageOperationFinalize(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	op, err := s.loadStorageOperation(r.Context(), pathID(r))
	if err != nil {
		writeFromError(w, err)
		return
	}
	if op.Status != "grace" {
		writeError(w, http.StatusConflict, "OPERATION", "Only a verified move in its grace window can be finalized")
		return
	}
	if err := s.FinalizeStorageOperation(r.Context(), op.ID, true); err != nil {
		writeError(w, http.StatusBadGateway, "FINALIZE", "Finalization failed: "+err.Error())
		return
	}
	s.actorAudit(r, sess, "storage.move_finalized", op.ID, "Finalized a mailbox move and removed the retained source", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "succeeded"})
}

func storageOperationSummary(op storageOperation) map[string]any {
	view := map[string]any{
		"id": op.ID, "kind": op.Kind, "status": op.Status, "step": op.Step,
		"progress": op.Progress, "totalBytes": op.TotalBytes, "movedBytes": op.MovedBytes,
		"error": op.Error, "cancelRequested": op.CancelRequested,
		"actorEmail": op.ActorEmail, "createdAt": op.CreatedAt.UTC().Format(time.RFC3339),
		"updatedAt":   op.UpdatedAt.UTC().Format(time.RFC3339),
		"canCancel":   op.Status == "planned" || op.Status == "queued" || op.Status == "running",
		"canRollback": op.Status == "grace" || op.Status == "failed" || op.Status == "running",
		"canFinalize": op.Status == "grace",
	}
	if op.StartedAt != nil {
		view["startedAt"] = op.StartedAt.UTC().Format(time.RFC3339)
	}
	if op.FinishedAt != nil {
		view["finishedAt"] = op.FinishedAt.UTC().Format(time.RFC3339)
	}
	if op.FinalizeAt != nil {
		view["finalizeAt"] = op.FinalizeAt.UTC().Format(time.RFC3339)
	}
	if strings.HasPrefix(op.Kind, "volume_") {
		var volumePlan storageVolumePlan
		if json.Unmarshal(op.Plan, &volumePlan) == nil {
			view["planName"] = volumePlan.Name
			view["planVolumeName"] = volumePlan.VolumeName
			view["planMountPath"] = volumePlan.MountPath
			view["planDevice"] = volumePlan.DevicePath
			view["planMode"] = volumePlan.Mode
			view["planCommands"] = volumePlan.Commands
			view["planWarnings"] = volumePlan.Warnings
			view["planConfirmValue"] = volumePlan.ConfirmValue
		}
		// Host operations are completed by the runner in one pass and cannot
		// use the mailbox grace/rollback lifecycle.
		view["canCancel"] = op.Status == "planned" || op.Status == "queued"
		view["canRollback"] = false
		view["canFinalize"] = false
		return view
	}
	var plan storageMovePlan
	if json.Unmarshal(op.Plan, &plan) == nil {
		view["scope"] = plan.Scope
		view["domain"] = plan.Domain
		view["targetVolume"] = plan.TargetVolumeName
		view["targetMountPath"] = plan.TargetMountPath
		view["mailboxes"] = len(plan.Items)
		view["verify"] = plan.Verify
		view["graceHours"] = plan.GraceHours
	}
	return view
}

func storageMoveItemView(item storageMoveItem) map[string]any {
	return map[string]any{
		"id": item.ID, "mailboxId": item.MailboxID, "email": item.Email,
		"sourceHome": item.SourceHome, "targetHome": item.TargetHome,
		"sourceVolume": item.SourceVolumeName, "targetVolume": item.TargetVolumeName,
		"status": item.Status, "bytes": item.Bytes, "messages": item.Messages,
		"error": item.Error,
	}
}

func (s *Server) auditStorageOperation(ctx context.Context, op storageOperation, action, detail string, metadata any) {
	if metadata == nil {
		metadata = map[string]any{}
	}
	var user *adminUser
	if strings.TrimSpace(op.ActorID) != "" || strings.TrimSpace(op.ActorEmail) != "" {
		user = &adminUser{ID: op.ActorID, Email: op.ActorEmail}
	}
	s.audit(ctx, user, action, op.ID, detail, nil, "", metadata)
}

func (s *Server) enqueueStorageOperation(operationID string) error {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("CONTROL_STORAGE_EXECUTOR")))
	runnerPath := strings.TrimSpace(s.cfg.StorageRunner)
	if mode == "direct" || mode == "inline" || runnerPath == "" || !pathExists(runnerPath) {
		go s.RunStorageOperation(context.Background(), operationID)
		return nil
	}
	if _, err := os.Stat("/usr/bin/systemd-run"); err != nil {
		go s.RunStorageOperation(context.Background(), operationID)
		return nil
	}
	unit := "a8m-storage-" + strings.ReplaceAll(operationID, "-", "")
	cmd := exec.Command("systemd-run",
		"--unit="+unit, "--collect", "--description=Alt8Mail storage move",
		"--property=EnvironmentFile=/etc/a8m/control.env",
		"--property=WorkingDirectory=/opt/alt8mail",
		"--property=Nice=10", "--property=IOSchedulingClass=best-effort",
		"--property=IOSchedulingPriority=7",
		runnerPath, "-operation", operationID,
	)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func uniqueNonEmpty(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
