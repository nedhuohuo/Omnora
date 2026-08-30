//go:build !windows

package mountid

import (
	"fmt"
	"os"
	"syscall"
)

func filesystemIdentity(path string, info os.FileInfo) (uint64, uint64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return 0, 0, fmt.Errorf("%w: stat_t unavailable for %q", ErrIdentityUnverifiable, path)
	}
	return uint64(stat.Dev), uint64(stat.Ino), nil
}
