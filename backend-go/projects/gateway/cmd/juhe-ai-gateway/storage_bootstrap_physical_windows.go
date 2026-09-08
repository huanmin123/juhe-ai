//go:build windows

// Windows half of the SQLite physical identity gate (D-44): os.Stat exposes
// no link count or inode here (same limitation Node documents for win32), so
// the hardlink gate degrades to a no-op and same-file duplicates are caught
// pairwise through os.SameFile in the shared gate logic.
package main

import (
	"os"
)

func requireUnlinkedSQLiteFile(role, path string, info os.FileInfo) error {
	return nil
}

func sqliteFileIdentity(info os.FileInfo) (string, bool) {
	return "", false
}
