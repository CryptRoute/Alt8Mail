package control

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

const storageFstabBeginPrefix = "# BEGIN A8M STORAGE"
const storageFstabEndPrefix = "# END A8M STORAGE"

var storageVolumeNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)

func validateStorageVolumeName(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if !storageVolumeNamePattern.MatchString(name) {
		return "", fmt.Errorf("volume name must be 1-48 lowercase letters, digits or hyphens and start with a letter or digit")
	}
	return name, nil
}

func defaultStorageMountPath(root, name string) string {
	return filepath.Join(filepath.Clean(root), name)
}

func validateStorageMountPath(root, mountPath string) (string, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	mountPath = filepath.Clean(strings.TrimSpace(mountPath))
	if root == "" || mountPath == "" || !filepath.IsAbs(mountPath) {
		return "", fmt.Errorf("mount path must be absolute")
	}
	if !pathWithin(root, mountPath) || sameStoragePath(root, mountPath) {
		return "", fmt.Errorf("mount path must be inside %s and must not be the storage root itself", root)
	}
	if strings.ContainsAny(mountPath, " \t\r\n") {
		return "", fmt.Errorf("mount path must not contain whitespace")
	}
	return mountPath, nil
}

func storageFstabBlock(name, fsUUID, mountPath, fsType string) string {
	options := "defaults,nofail,noatime,nodev,nosuid,x-systemd.device-timeout=15s"
	return fmt.Sprintf("%s volume=%s name=%s\nUUID=%s %s %s %s 0 2\n%s volume=%s name=%s",
		storageFstabBeginPrefix, fsUUID, name, fsUUID, mountPath, fsType, options,
		storageFstabEndPrefix, fsUUID, name)
}

// upsertStorageFstabBlock removes any managed block with the same name or
// filesystem UUID, then appends the new block. It never rewrites an
// unmanaged fstab line.
func upsertStorageFstabBlock(content, block, name, fsUUID string) (string, error) {
	cleaned, err := removeStorageFstabBlocks(content, name, fsUUID)
	if err != nil {
		return "", err
	}
	cleaned = strings.TrimRight(cleaned, "\n")
	if cleaned != "" {
		cleaned += "\n"
	}
	return cleaned + block + "\n", nil
}

func removeStorageFstabBlocks(content, name, fsUUID string) (string, error) {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	insideAny := false
	insideRemove := false
	removed := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, storageFstabBeginPrefix) {
			if insideAny {
				return "", fmt.Errorf("fstab contains nested Alt8Mail storage markers")
			}
			insideAny = true
			insideRemove = storageFstabMarkerMatches(trimmed, name, fsUUID)
			if insideRemove {
				removed = true
				continue
			}
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(trimmed, storageFstabEndPrefix) {
			if !insideAny {
				return "", fmt.Errorf("fstab contains an Alt8Mail storage end marker without a begin marker")
			}
			insideAny = false
			if insideRemove {
				insideRemove = false
				continue
			}
			out = append(out, line)
			continue
		}
		if insideAny {
			if !insideRemove {
				out = append(out, line)
			}
			continue
		}
		out = append(out, line)
	}
	if insideAny {
		return "", fmt.Errorf("fstab contains an unterminated Alt8Mail storage block")
	}
	if !removed {
		return content, nil
	}
	result := strings.Join(out, "\n")
	if result != "" && !strings.HasSuffix(result, "\n") {
		result += "\n"
	}
	return result, nil
}

func storageFstabMarkerMatches(marker, name, fsUUID string) bool {
	for _, field := range strings.Fields(marker) {
		if name != "" && field == "name="+name {
			return true
		}
		if fsUUID != "" && field == "volume="+fsUUID {
			return true
		}
	}
	return false
}

func storageMountUnitName(mountPath string) string {
	// systemd-escape produces the authoritative escaped unit. This fallback is
	// only used when systemd-escape is unavailable; callers normally replace it.
	clean := strings.Trim(strings.ReplaceAll(filepath.ToSlash(mountPath), "/", "-"), "-")
	return clean + ".mount"
}
