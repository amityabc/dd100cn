//go:build !windows

package main

import (
	"errors"
	"os"
	"time"
)

var processTicks = time.Now().UnixNano()/100 + 621355968000000000

func isReparse(f os.FileInfo) bool { return f.Mode()&os.ModeSymlink != 0 }

func processIdentity(pid int) (string, int64, error) {
	if pid != os.Getpid() {
		return "", 0, errors.New("legacy launcher process identity requires Windows")
	}
	exe, err := os.Executable()
	return exe, processTicks, err
}

func replaceFile(src, dst string) error    { return os.Rename(src, dst) }
func publishNewFile(src, dst string) error { return os.Link(src, dst) }
