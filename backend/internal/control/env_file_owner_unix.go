//go:build !windows

package control

import (
	"os"
	"syscall"
)

func envFileOwner(info os.FileInfo) (int, int, bool) {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(stat.Uid), int(stat.Gid), true
	}
	return 0, 0, false
}

func chownEnvFile(path string, uid, gid int) error {
	return os.Chown(path, uid, gid)
}
