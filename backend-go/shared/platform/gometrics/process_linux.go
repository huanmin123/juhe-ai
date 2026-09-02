//go:build linux

package gometrics

import (
	"bytes"
	"os"
	"strconv"
	"syscall"
)

func readProcessCPUSeconds() (float64, bool) {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0, false
	}
	return syscallTimevalSeconds(usage.Utime) + syscallTimevalSeconds(usage.Stime), true
}

func syscallTimevalSeconds(value syscall.Timeval) float64 {
	return float64(value.Sec) + float64(value.Usec)/1e6
}

func readRSSBytes() uint64 {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := bytes.Fields(data)
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(string(fields[1]), 10, 64)
	if err != nil {
		return 0
	}
	return pages * uint64(os.Getpagesize())
}

func readFDCount() uint64 {
	dir, err := os.Open("/proc/self/fd")
	if err != nil {
		return 0
	}
	names, err := dir.Readdirnames(-1)
	_ = dir.Close()
	if err != nil {
		return 0
	}
	return uint64(len(names))
}
