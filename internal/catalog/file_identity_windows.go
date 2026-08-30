//go:build windows

package catalog

import "os"

func catalogFileIdentity(os.FileInfo) (uint64, uint64, bool) {
	return 0, 0, false
}
