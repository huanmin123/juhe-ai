//go:build !windows && !linux && !darwin

package managementruntimeloggrep

import (
	"os"
	"time"
)

func fileStartTime(_ string, _ os.FileInfo, _ time.Time) time.Time {
	return time.Time{}
}
