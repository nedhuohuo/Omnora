//go:build windows

package mountid

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func filesystemIdentity(path string, _ os.FileInfo) (uint64, uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: open %q: %v", ErrIdentityUnverifiable, path, err)
	}
	defer file.Close()

	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return 0, 0, fmt.Errorf("%w: file identity %q: %v", ErrIdentityUnverifiable, path, err)
	}
	inode := uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)
	return uint64(info.VolumeSerialNumber), inode, nil
}
