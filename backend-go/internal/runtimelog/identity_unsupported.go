//go:build !windows && !linux && !darwin

package runtimelog

import (
	"fmt"
	"os"
)

func FileIdentity(_ string, _ os.FileInfo) (string, error) {
	return "", fmt.Errorf("当前平台尚未实现 Node 兼容的 runtime log file identity")
}
