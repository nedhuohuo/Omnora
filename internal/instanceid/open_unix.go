//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package instanceid

import (
	"os"

	"golang.org/x/sys/unix"
)

func openMarkerNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
