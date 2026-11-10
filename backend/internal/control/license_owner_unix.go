//go:build !windows

package control

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"
	"syscall"
)

// resolveLicenseFileOwner decides the ownership to apply to the temporary
// license file before it is renamed over the target.
//
// The Control service runs as root in production while the webmail runs as the
// unprivileged a8m user, so the installed token must be group-readable. The
// owner uid is preserved when a target already exists, but the group is always
// normalized to the configured group (a8m by default). That repairs a legacy
// root:root token on the next upload instead of silently leaving it unreadable
// by the webmail. Non-root development installs skip chown because they cannot
// change ownership.
func resolveLicenseFileOwner(path, group string) (licenseFileOwner, error) {
	if os.Geteuid() != 0 {
		return licenseFileOwner{}, nil
	}
	uid := 0
	if info, err := os.Stat(path); err == nil {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			uid = int(stat.Uid)
		}
	}
	gid, err := lookupLicenseGroupID(group)
	if err != nil {
		return licenseFileOwner{}, err
	}
	return licenseFileOwner{uid: uid, gid: gid, apply: true}, nil
}

func lookupLicenseGroupID(group string) (int, error) {
	group = strings.TrimSpace(group)
	if group == "" {
		group = "a8m"
	}
	if gid, err := strconv.Atoi(group); err == nil {
		return gid, nil
	}
	entry, err := user.LookupGroup(group)
	if err != nil {
		return 0, fmt.Errorf("look up license group: %w", err)
	}
	gid, err := strconv.Atoi(entry.Gid)
	if err != nil {
		return 0, fmt.Errorf("parse license group id: %w", err)
	}
	return gid, nil
}

func (o licenseFileOwner) applyTo(path string) error {
	if !o.apply {
		return nil
	}
	return os.Chown(path, o.uid, o.gid)
}
