package mailstorage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
)

type fakeRow struct {
	id, name, mount, state string
	err                    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*string)) = r.id
	*(dest[1].(*string)) = r.name
	*(dest[2].(*string)) = r.mount
	*(dest[3].(*string)) = r.state
	return nil
}

type fakeQuerier struct{ row pgx.Row }

func (q fakeQuerier) QueryRow(context.Context, string, ...any) pgx.Row { return q.row }

func TestResolveDomainUsesAssignedVolume(t *testing.T) {
	placement, err := ResolveDomain(context.Background(), fakeQuerier{row: fakeRow{
		id: "11111111-1111-1111-1111-111111111111", name: "mail-02",
		mount: "/srv/a8m-storage/mail-02", state: "online",
	}}, "example.com", "/var/vmail")
	if err != nil {
		t.Fatal(err)
	}
	if placement.Legacy || placement.VolumeName != "mail-02" || placement.MountPath != filepath.FromSlash("/srv/a8m-storage/mail-02") {
		t.Fatalf("unexpected placement: %+v", placement)
	}
	home, maildir := placement.Home("example.com", "alice")
	if home != filepath.Join(filepath.FromSlash("/srv/a8m-storage/mail-02"), "example.com", "alice") {
		t.Fatalf("unexpected home %q", home)
	}
	if maildir != filepath.Join(home, "Maildir") {
		t.Fatalf("unexpected maildir %q", maildir)
	}
}

func TestResolveDomainFallsBackToLegacy(t *testing.T) {
	placement, err := ResolveDomain(context.Background(), fakeQuerier{row: fakeRow{err: pgx.ErrNoRows}}, "example.com", "/var/vmail")
	if err != nil {
		t.Fatal(err)
	}
	if !placement.Legacy || placement.MountPath != filepath.FromSlash("/var/vmail") {
		t.Fatalf("unexpected fallback: %+v", placement)
	}
}

func TestResolveDomainTreatsLegacyMountAsLegacy(t *testing.T) {
	placement, err := ResolveDomain(context.Background(), fakeQuerier{row: fakeRow{
		id: "22222222-2222-2222-2222-222222222222", name: "legacy",
		mount: "/var/vmail/", state: "online",
	}}, "example.com", "/var/vmail")
	if err != nil {
		t.Fatal(err)
	}
	if !placement.Legacy {
		t.Fatalf("expected legacy placement, got %+v", placement)
	}
}

func TestResolveDomainPropagatesDatabaseError(t *testing.T) {
	want := errors.New("database unavailable")
	_, err := ResolveDomain(context.Background(), fakeQuerier{row: fakeRow{err: want}}, "example.com", "/var/vmail")
	if !errors.Is(err, want) {
		t.Fatalf("expected database error, got %v", err)
	}
}
