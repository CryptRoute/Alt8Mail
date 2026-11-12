// Package mailstorage resolves the effective mail home for a mailbox from the
// Control Plane's storage registry. It deliberately does not read
// mailboxes.maildir_path: that column can be stale on older installations.
package mailstorage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Querier is implemented by both pgxpool.Pool and pgx.Tx.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Placement struct {
	VolumeID   string
	VolumeName string
	MountPath  string
	State      string
	Legacy     bool
}

// ResolveDomain returns the domain's effective volume, then the global default,
// then the legacy root. A missing domain row is not an error; it falls back to
// the legacy root so callers can still provision by address.
func ResolveDomain(ctx context.Context, q Querier, domain, legacyRoot string) (Placement, error) {
	legacyRoot = filepath.Clean(strings.TrimSpace(legacyRoot))
	var id, name, mount, state string
	err := q.QueryRow(ctx, `
		SELECT COALESCE(v.id::text, ''), COALESCE(v.name, ''),
		       COALESCE(v.mount_path, ''), COALESCE(v.state, '')
		FROM public.mail_domains d
		LEFT JOIN control.storage_volumes v
		  ON v.id = COALESCE(
		    d.storage_volume_id,
		    (SELECT default_volume_id FROM control.storage_policy WHERE id = true)
		  )
		WHERE d.domain = $1
	`, domain).Scan(&id, &name, &mount, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return Placement{MountPath: legacyRoot, Legacy: true}, nil
	}
	if err != nil {
		return Placement{}, err
	}
	mount = filepath.Clean(strings.TrimSpace(mount))
	if mount == "." || mount == "" {
		mount = legacyRoot
	}
	return Placement{
		VolumeID:   id,
		VolumeName: name,
		MountPath:  mount,
		State:      state,
		Legacy:     id == "" || samePath(mount, legacyRoot),
	}, nil
}

// Home returns the mailbox home and its Maildir path. The home is the unit of
// placement because Dovecot also keeps Sieve, maildirsize and indexes there.
func (p Placement) Home(domain, local string) (home, maildir string) {
	home = filepath.Join(p.MountPath, domain, local)
	return home, filepath.Join(home, "Maildir")
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	absA, errA := filepath.Abs(filepath.Clean(a))
	absB, errB := filepath.Abs(filepath.Clean(b))
	return errA == nil && errB == nil && absA == absB
}
