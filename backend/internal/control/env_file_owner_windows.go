//go:build windows

package control

import "os"

func envFileOwner(os.FileInfo) (int, int, bool) { return 0, 0, false }
func chownEnvFile(string, int, int) error       { return nil }
