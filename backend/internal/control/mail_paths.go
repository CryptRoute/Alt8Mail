package control

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

func (s *Server) managedMailRoots(ctx context.Context) []string {
	roots := []string{}
	seen := map[string]bool{}
	add := func(root string) {
		root = filepath.Clean(strings.TrimSpace(root))
		if root == "" || root == "." || seen[root] {
			return
		}
		seen[root] = true
		roots = append(roots, root)
	}
	add(s.cfg.VmailRoot)
	if volumes, err := s.storageVolumeRows(ctx); err == nil {
		for _, volume := range volumes {
			if volume.Role == "mail" {
				add(volume.MountPath)
			}
		}
	}
	return roots
}

func (s *Server) isManagedMailPath(ctx context.Context, path string) bool {
	for _, root := range s.managedMailRoots(ctx) {
		if pathWithin(root, path) {
			return true
		}
	}
	return false
}

func (s *Server) isMailboxHomePath(ctx context.Context, path string) bool {
	for _, root := range s.managedMailRoots(ctx) {
		if pathWithin(root, path) && !sameStoragePath(root, path) {
			return true
		}
	}
	return false
}

func chownTree(root string, uid, gid int) error {
	if uid < 0 {
		return nil
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		return os.Chown(path, uid, gid)
	})
}
