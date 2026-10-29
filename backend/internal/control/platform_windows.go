//go:build windows

package control

import "os"

func fileInode(info os.FileInfo) uint64 { return 0 }

func filesystemUsage(path string) (used, total uint64, percent float64) { return 0, 0, 0 }
