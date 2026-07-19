//go:build windows

package ownerlock

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockFile(file *os.File) (func(*os.File) error, error) {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&overlapped,
	)
	if err != nil {
		return nil, err
	}
	return func(locked *os.File) error {
		return windows.UnlockFileEx(windows.Handle(locked.Fd()), 0, 1, 0, &overlapped)
	}, nil
}
