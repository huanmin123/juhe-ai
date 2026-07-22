//go:build darwin

package managementruntimeloggrep

import (
	"os"
	"syscall"
	"time"
)

func fileStartTime(_ string, info os.FileInfo, modified time.Time) time.Time {
	data, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}
	}
	created := time.Unix(data.Birthtimespec.Sec, data.Birthtimespec.Nsec).UTC()
	if created.IsZero() || created.After(modified) {
		return time.Time{}
	}
	return created
}
