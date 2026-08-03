//go:build linux

package mountid

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func statxIdentity(path string) (StatxInfo, error) {
	var stat unix.Statx_t
	mask := unix.STATX_INO | unix.STATX_MNT_ID
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, mask, &stat); err != nil {
		return StatxInfo{}, fmt.Errorf("%w: statx %q: %v", ErrIdentityUnverifiable, path, err)
	}
	if stat.Ino == 0 || stat.Mnt_id == 0 {
		return StatxInfo{}, fmt.Errorf("%w: statx identity incomplete for %q", ErrIdentityUnverifiable, path)
	}
	return StatxInfo{
		Available:   true,
		Mask:        stat.Mask,
		DeviceMajor: stat.Dev_major,
		DeviceMinor: stat.Dev_minor,
		Inode:       stat.Ino,
		MountID:     stat.Mnt_id,
	}, nil
}
