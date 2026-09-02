//go:build darwin

package gometrics

import (
	"os"
	"syscall"
)

// Darwin exposes process CPU time and maximum resident set size through
// getrusage. Maxrss is already expressed in bytes on macOS (unlike Linux,
// where the kernel reports it in KiB).
func readProcessCPUSeconds() (float64, bool) {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0, false
	}
	return darwinTimevalSeconds(usage.Utime) + darwinTimevalSeconds(usage.Stime), true
}

func darwinTimevalSeconds(value syscall.Timeval) float64 {
	return float64(value.Sec) + float64(value.Usec)/1e6
}

func readRSSBytes() uint64 {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil || usage.Maxrss <= 0 {
		return 0
	}
	return uint64(usage.Maxrss)
}

func readFDCount() uint64 {
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		return 0
	}
	return uint64(len(entries))
}
