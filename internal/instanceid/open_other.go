//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package instanceid

import "os"

func openMarkerNoFollow(path string) (*os.File, error) {
	return os.Open(path)
}
