//go:build linux

package managementruntimeloggrep

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func fileStartTime(path string, _ os.FileInfo, modified time.Time) time.Time {
	var stat unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BTIME, &stat); err != nil || stat.Mask&unix.STATX_BTIME == 0 {
		return time.Time{}
	}
	created := time.Unix(stat.Btime.Sec, int64(stat.Btime.Nsec)).UTC()
	if created.IsZero() || created.After(modified) {
		return time.Time{}
	}
	return created
}
