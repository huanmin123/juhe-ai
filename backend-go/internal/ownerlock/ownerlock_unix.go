//go:build !windows

package ownerlock

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockFile(file *os.File) (func(*os.File) error, error) {
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, err
	}
	return func(locked *os.File) error {
		return unix.Flock(int(locked.Fd()), unix.LOCK_UN)
	}, nil
}
