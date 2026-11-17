package control

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/CryptRoute/Alt8Mail/backend/internal/mailstorage"
	"github.com/jackc/pgx/v5"
)

type storagePlacement struct {
	VolumeID   string
	VolumeName string
	MountPath  string
	Legacy     bool
}

func (s *Server) validateStorageVolumeForPlacement(ctx context.Context, volumeID string) (storageVolume, error) {
	volume, err := scanStorageVolume(s.db.QueryRow(ctx, `
		SELECT `+storageVolumeColumns+` FROM control.storage_volumes WHERE id = $1
	`, volumeID))
	if errors.Is(err, pgx.ErrNoRows) {
		return storageVolume{}, fail(http.StatusBadRequest, "STORAGE_VOLUME", "Choose a registered storage volume")
	}
	if err != nil {
		return storageVolume{}, err
	}
	if volume.State != "online" {
		return storageVolume{}, fail(http.StatusConflict, "STORAGE_VOLUME", "That storage volume is not online. Check the mount and try again.")
	}
	legacy := sameStoragePath(volume.MountPath, s.cfg.VmailRoot)
	if !legacy {
		doctor := s.storageDoctor(ctx)
		if !doctor.UserDBReady {
			return storageVolume{}, fail(http.StatusConflict, "STORAGE_DOCTOR", "Dovecot does not read per-mailbox homes yet. Apply the managed userdb query before placing mail on another volume.")
		}
	}
	return volume, nil
}

func (s *Server) placementForDomain(ctx context.Context, domain, requestedVolumeID string) (storagePlacement, error) {
	if strings.TrimSpace(requestedVolumeID) != "" {
		volume, err := s.validateStorageVolumeForPlacement(ctx, strings.TrimSpace(requestedVolumeID))
		if err != nil {
			return storagePlacement{}, err
		}
		return storagePlacement{
			VolumeID: volume.ID, VolumeName: volume.Name, MountPath: volume.MountPath,
			Legacy: sameStoragePath(volume.MountPath, s.cfg.VmailRoot),
		}, nil
	}
	placement, err := mailstorage.ResolveDomain(ctx, s.db, domain, s.cfg.VmailRoot)
	if err != nil {
		return storagePlacement{}, err
	}
	if placement.State != "" && placement.State != "online" {
		return storagePlacement{}, fail(http.StatusConflict, "STORAGE_VOLUME", "The storage volume assigned to this domain is not online. Check the mount or change the domain placement.")
	}
	if !placement.Legacy {
		doctor := s.storageDoctor(ctx)
		if !doctor.UserDBReady {
			return storagePlacement{}, fail(http.StatusConflict, "STORAGE_DOCTOR", "Dovecot does not read per-mailbox homes yet. Apply the managed userdb query before placing mail on another volume.")
		}
	}
	return storagePlacement{
		VolumeID: placement.VolumeID, VolumeName: placement.VolumeName,
		MountPath: placement.MountPath, Legacy: placement.Legacy,
	}, nil
}

func (s *Server) handleStoragePlacements(w http.ResponseWriter, r *http.Request, _ *sessionRecord) {
	if err := s.ensureStorageVolumes(r.Context()); err != nil {
		writeFromError(w, err)
		return
	}
	policy, err := s.storagePolicyRow(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	volumes, err := s.storageVolumeRows(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	volumeOptions := make([]map[string]any, 0, len(volumes))
	for _, volume := range volumes {
		if volume.State == "retired" || volume.State == "wiped" {
			continue
		}
		volumeOptions = append(volumeOptions, map[string]any{
			"id": volume.ID, "name": volume.Name, "mountPath": volume.MountPath,
			"state": volume.State, "legacy": sameStoragePath(volume.MountPath, s.cfg.VmailRoot),
		})
	}
	rows, err := s.db.Query(r.Context(), `
		SELECT d.id::text, d.domain::text, COALESCE(d.storage_volume_id::text, ''),
		       COALESCE(v.name, ''), COALESCE(v.mount_path, ''),
		       (SELECT count(*) FROM public.mailboxes m WHERE m.domain = d.domain),
		       (SELECT count(*) FROM public.mailboxes m WHERE m.domain = d.domain AND m.storage_volume_id IS NOT NULL)
		FROM public.mail_domains d
		LEFT JOIN control.storage_volumes v
		  ON v.id = COALESCE(
		    d.storage_volume_id,
		    (SELECT default_volume_id FROM control.storage_policy WHERE id = true)
		  )
		WHERE d.status IN ('verified', 'manual')
		ORDER BY d.domain
	`)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	domains := []map[string]any{}
	for rows.Next() {
		var id, domain, volumeID, volumeName, mountPath string
		var mailboxCount, placedCount int64
		if err := rows.Scan(&id, &domain, &volumeID, &volumeName, &mountPath, &mailboxCount, &placedCount); err != nil {
			writeFromError(w, err)
			return
		}
		legacy := volumeID == "" || sameStoragePath(mountPath, s.cfg.VmailRoot)
		domains = append(domains, map[string]any{
			"id": id, "domain": domain, "volumeId": volumeID,
			"volumeName": volumeName, "mountPath": mountPath,
			"mailboxCount": mailboxCount, "placedCount": placedCount,
			"legacy": legacy,
		})
	}
	if err := rows.Err(); err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"policy":  policy,
		"volumes": volumeOptions,
		"domains": domains,
		"doctor":  s.storageDoctor(r.Context()),
	})
}

func (s *Server) handleStorageAssignment(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Scope    string `json:"scope"`
		ID       string `json:"id"`
		VolumeID string `json:"volumeId"`
	}
	if err := decodeJSON(w, r, &body, 32<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	scope := strings.ToLower(strings.TrimSpace(body.Scope))
	volumeID := strings.TrimSpace(body.VolumeID)
	if volumeID != "" {
		if _, err := s.validateStorageVolumeForPlacement(r.Context(), volumeID); err != nil {
			writeFromError(w, err)
			return
		}
	}
	switch scope {
	case "global":
		if _, err := s.db.Exec(r.Context(), `
			UPDATE control.storage_policy
			SET default_volume_id = NULLIF($1, '')::uuid, updated_at = now()
			WHERE id = true
		`, volumeID); err != nil {
			writeFromError(w, err)
			return
		}
		s.actorAudit(r, sess, "storage.default_changed", "global", "Changed the default storage volume for new mailboxes", map[string]any{"volumeId": volumeID})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "scope": "global", "volumeId": volumeID})
	case "domain":
		id := strings.TrimSpace(body.ID)
		if id == "" {
			writeError(w, http.StatusBadRequest, "DOMAIN", "Choose a domain")
			return
		}
		var domain string
		if err := s.db.QueryRow(r.Context(), `SELECT domain::text FROM public.mail_domains WHERE id = $1`, id).Scan(&domain); err != nil {
			writeFromError(w, err)
			return
		}
		if _, err := s.db.Exec(r.Context(), `
			UPDATE public.mail_domains SET storage_volume_id = NULLIF($1, '')::uuid WHERE id = $2
		`, volumeID, id); err != nil {
			writeFromError(w, err)
			return
		}
		var mailboxCount int64
		_ = s.db.QueryRow(r.Context(), `SELECT count(*) FROM public.mailboxes WHERE domain = $1`, domain).Scan(&mailboxCount)
		s.actorAudit(r, sess, "storage.domain_changed", domain, "Changed the default storage volume for new mailboxes", map[string]any{"volumeId": volumeID, "existingMailboxes": mailboxCount})
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "scope": "domain", "domain": domain, "volumeId": volumeID,
			"existingMailboxes": mailboxCount, "appliesToNewMailboxes": true,
			"message": "This changes the default for new mailboxes only. Existing mail stays where it is until you start a move.",
		})
	default:
		writeError(w, http.StatusBadRequest, "SCOPE", "Use scope 'global' or 'domain'")
	}
}

func sameStoragePath(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	absA, errA := filepath.Abs(filepath.Clean(a))
	absB, errB := filepath.Abs(filepath.Clean(b))
	return errA == nil && errB == nil && absA == absB
}
