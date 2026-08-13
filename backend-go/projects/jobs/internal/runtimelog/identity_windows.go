//go:build windows

package runtimelog

import (
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/windows"
)

const windowsToUnixEpoch100Nanoseconds = 116444736000000000

func FileIdentity(path string, _ os.FileInfo) (string, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	var details windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &details); err != nil {
		return "", err
	}
	fileIndex := uint64(details.FileIndexHigh)<<32 | uint64(details.FileIndexLow)
	creationTicks := uint64(details.CreationTime.HighDateTime)<<32 | uint64(details.CreationTime.LowDateTime)
	creationMs := int64((creationTicks - windowsToUnixEpoch100Nanoseconds) / 10000)
	// Node fs.Stats exposes ino as a JavaScript number, so high file indexes carry its IEEE-754 rounding.
	nodeInode := strconv.FormatFloat(float64(fileIndex), 'f', -1, 64)
	return fmt.Sprintf("%d:%s:%d", details.VolumeSerialNumber, nodeInode, creationMs), nil
}
