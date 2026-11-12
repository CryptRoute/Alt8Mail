//go:build windows

package control

import (
	"errors"
	"os"
)

var errStorageUnsupported = errors.New("storage metrics are unavailable on this platform")

type filesystemStat struct {
	TotalBytes   uint64
	UsedBytes    uint64
	FreeBytes    uint64
	InodeTotal   uint64
	InodeUsed    uint64
	UsedPercent  float64
	InodePercent float64
}

type mountEntry struct {
	Device     string
	MountPoint string
	FSType     string
	Options    string
}

func fileInode(info os.FileInfo) uint64 { return 0 }

func chownPath(path string, uid, gid int) error { return nil }

func fileOwner(path string) (int, int, bool) { return 0, 0, false }

func filesystemUsage(path string) (used, total uint64, percent float64) { return 0, 0, 0 }

func readFilesystemStat(path string) (filesystemStat, error) {
	return filesystemStat{}, errStorageUnsupported
}

func readMountEntries() ([]mountEntry, error) {
	return nil, errStorageUnsupported
}
