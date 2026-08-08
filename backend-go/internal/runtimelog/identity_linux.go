//go:build linux

package runtimelog

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func FileIdentity(path string, info os.FileInfo) (string, error) {
	stats, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("无法读取 Linux 文件 identity")
	}
	birthtimeMs := stats.Ctim.Sec*1000 + stats.Ctim.Nsec/1_000_000
	var statx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_STATX_SYNC_AS_STAT, unix.STATX_BTIME, &statx); err == nil && statx.Mask&unix.STATX_BTIME != 0 {
		birthtimeMs = int64(statx.Btime.Sec)*1000 + int64(statx.Btime.Nsec)/1_000_000
	}
	return fmt.Sprintf("%d:%d:%d", stats.Dev, stats.Ino, birthtimeMs), nil
}
