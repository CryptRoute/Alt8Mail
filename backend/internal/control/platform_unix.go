//go:build !windows

package control

import (
	"bufio"
	"os"
	"strings"
	"syscall"
)

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

func readFilesystemStat(path string) (filesystemStat, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return filesystemStat{}, err
	}
	out := filesystemStat{
		TotalBytes: stat.Blocks * uint64(stat.Bsize),
		FreeBytes:  stat.Bavail * uint64(stat.Bsize),
		InodeTotal: stat.Files,
	}
	if out.TotalBytes >= out.FreeBytes {
		out.UsedBytes = out.TotalBytes - out.FreeBytes
	}
	if out.TotalBytes > 0 {
		out.UsedPercent = float64(out.UsedBytes) / float64(out.TotalBytes) * 100
	}
	if out.InodeTotal >= stat.Ffree {
		out.InodeUsed = out.InodeTotal - stat.Ffree
	}
	if out.InodeTotal > 0 {
		out.InodePercent = float64(out.InodeUsed) / float64(out.InodeTotal) * 100
	}
	return out, nil
}

func readMountEntries() ([]mountEntry, error) {
	raw, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return nil, err
	}
	decoder := strings.NewReplacer(
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
		`\134`, `\`,
	)
	entries := []mountEntry{}
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}
		entries = append(entries, mountEntry{
			Device:     decoder.Replace(fields[0]),
			MountPoint: decoder.Replace(fields[1]),
			FSType:     decoder.Replace(fields[2]),
			Options:    decoder.Replace(fields[3]),
		})
	}
	return entries, scanner.Err()
}

func fileInode(info os.FileInfo) uint64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return stat.Ino
	}
	return 0
}

func chownPath(path string, uid, gid int) error {
	if uid < 0 && gid < 0 {
		return nil
	}
	return os.Chown(path, uid, gid)
}

func fileOwner(path string) (int, int, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(stat.Uid), int(stat.Gid), true
}

func filesystemUsage(path string) (used, total uint64, percent float64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, 0
	}
	total = stat.Blocks * uint64(stat.Bsize)
	free := stat.Bavail * uint64(stat.Bsize)
	if total >= free {
		used = total - free
	}
	if total > 0 {
		percent = float64(used) / float64(total) * 100
	}
	return used, total, percent
}
