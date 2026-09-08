//go:build unix

// POSIX half of the SQLite physical identity gate (D-44): the link count and
// dev:ino identity come from the standard stat payload.
package main

import (
	"fmt"
	"os"
	"syscall"
)

// requireUnlinkedSQLiteFile enforces the nlink=1 hardlink gate
// (Node existingSqliteFileIdentity: 路径包含硬链接，无法证明单文件单 owner).
func requireUnlinkedSQLiteFile(role, path string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return fmt.Errorf("%s 的 SQLite 路径无法提供稳定的物理文件 identity：%s", role, path)
	}
	if stat.Nlink != 1 {
		return fmt.Errorf("%s 的 SQLite 路径包含硬链接，无法证明单文件单 owner：%s", role, path)
	}
	return nil
}

// sqliteFileIdentity returns "dev:ino" when the stat payload is usable.
func sqliteFileIdentity(info os.FileInfo) (string, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil || stat.Ino <= 0 {
		return "", false
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), true
}
