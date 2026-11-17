//go:build windows

package control

// Windows has no POSIX uid/gid model. Development installs use the default
// 0640 mode (mapped to a writable file) and skip ownership changes.
func resolveLicenseFileOwner(path, group string) (licenseFileOwner, error) {
	return licenseFileOwner{}, nil
}

func (o licenseFileOwner) applyTo(path string) error { return nil }
