//go:build !windows

package catalog

import (
	"os"
	"syscall"
)

func catalogFileIdentity(info os.FileInfo) (uint64, uint64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return 0, 0, false
	}
	return uint64(stat.Dev), uint64(stat.Ino), true
}
