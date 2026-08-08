//go:build darwin

package runtimelog

import (
	"fmt"
	"os"
	"syscall"
)

func FileIdentity(_ string, info os.FileInfo) (string, error) {
	stats, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("无法读取 macOS 文件 identity")
	}
	birthtimeMs := stats.Birthtimespec.Sec*1000 + stats.Birthtimespec.Nsec/1_000_000
	return fmt.Sprintf("%d:%d:%d", stats.Dev, stats.Ino, birthtimeMs), nil
}
