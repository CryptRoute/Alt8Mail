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
// unprivileged a8m user, so the installed token must be group-readable. When a
// target already exists its uid/gid are preserved; otherwise root-owned files
// use the configured group (a8m by default). Non-root development installs
// skip chown because they cannot change ownership.
func resolveLicenseFileOwner(path, group string) (licenseFileOwner, error) {
	root := os.Geteuid() == 0
	if info, err := os.Stat(path); err == nil {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			if !root {
				return licenseFileOwner{}, nil
			}
			return licenseFileOwner{uid: int(stat.Uid), gid: int(stat.Gid), apply: true}, nil
		}
	}
	if !root {
		return licenseFileOwner{}, nil
	}
	gid, err := lookupLicenseGroupID(group)
	if err != nil {
		return licenseFileOwner{}, err
	}
	return licenseFileOwner{uid: 0, gid: gid, apply: true}, nil
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
