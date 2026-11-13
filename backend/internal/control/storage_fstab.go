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

const storageSwapFstabBeginPrefix = "# BEGIN A8M SWAP"
const storageSwapFstabEndPrefix = "# END A8M SWAP"

func validateStorageSwapName(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if !storageVolumeNamePattern.MatchString(name) {
		return "", fmt.Errorf("swap name must be 1-48 lowercase letters, digits or hyphens and start with a letter or digit")
	}
	return name, nil
}

func storageSwapFstabBlock(name, source, device string) string {
	return fmt.Sprintf("%s name=%s device=%s\n%s none swap sw 0 0\n%s name=%s device=%s",
		storageSwapFstabBeginPrefix, name, device, source,
		storageSwapFstabEndPrefix, name, device)
}

func upsertStorageSwapFstabBlock(content, block, name, device string) (string, error) {
	cleaned, err := removeStorageSwapFstabBlocks(content, name, device)
	if err != nil {
		return "", err
	}
	cleaned = strings.TrimRight(cleaned, "\n")
	if cleaned != "" {
		cleaned += "\n"
	}
	return cleaned + block + "\n", nil
}

func removeStorageSwapFstabBlocks(content, name, device string) (string, error) {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	insideAny := false
	insideRemove := false
	removed := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, storageSwapFstabBeginPrefix) {
			if insideAny {
				return "", fmt.Errorf("fstab contains nested Alt8Mail swap markers")
			}
			insideAny = true
			insideRemove = storageSwapFstabMarkerMatches(trimmed, name, device)
			if insideRemove {
				removed = true
				continue
			}
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(trimmed, storageSwapFstabEndPrefix) {
			if !insideAny {
				return "", fmt.Errorf("fstab contains an Alt8Mail swap end marker without a begin marker")
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
		return "", fmt.Errorf("fstab contains an unterminated Alt8Mail swap block")
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

func storageSwapFstabMarkerMatches(marker, name, device string) bool {
	for _, field := range strings.Fields(marker) {
		if name != "" && field == "name="+name {
			return true
		}
		if device != "" && field == "device="+device {
			return true
		}
	}
	return false
}

// storageSwapManagedDevices returns the devices and file paths recorded in
// managed Alt8Mail swap blocks. The marker is intentionally independent of
// UUIDs: /proc/swaps reports the kernel device path, while fstab may use a
// UUID for the same swap area.
func storageSwapManagedDevices(content string) map[string]bool {
	managed := map[string]bool{}
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, storageSwapFstabBeginPrefix) {
			continue
		}
		for _, field := range strings.Fields(trimmed) {
			if strings.HasPrefix(field, "device=") && len(field) > len("device=") {
				managed[strings.TrimPrefix(field, "device=")] = true
			}
		}
	}
	return managed
}

// storageSwapFstabHasSource reports whether an unmanaged fstab swap line names
// the given source path. Managed lines are matched through their marker above.
func storageSwapFstabHasSource(content, source string) bool {
	source = strings.TrimSpace(source)
	if source == "" {
		return false
	}
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) >= 3 && fields[0] == source && fields[2] == "swap" {
			return true
		}
	}
	return false
}
