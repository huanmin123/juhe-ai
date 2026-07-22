//go:build windows

package managementruntimeloggrep

import (
	"os"
	"syscall"
	"time"
)

func fileStartTime(_ string, info os.FileInfo, modified time.Time) time.Time {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return modified
	}
	created := time.Unix(0, data.CreationTime.Nanoseconds()).UTC()
	if created.IsZero() || created.After(modified) {
		return modified
	}
	return created
}
