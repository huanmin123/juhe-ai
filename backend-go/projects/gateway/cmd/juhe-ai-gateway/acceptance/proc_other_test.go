//go:build !windows

package acceptance

import (
	"os/exec"
	"syscall"
)

// windowsNewProcessGroupAttr 仅在 Windows 构建时有意义。
func windowsNewProcessGroupAttr() *syscall.SysProcAttr { return nil }

// interruptProcess 在 POSIX 上以 SIGTERM 触发 supervisor 的干净停机。
func interruptProcess(cmd *exec.Cmd) error {
	return cmd.Process.Signal(syscall.SIGTERM)
}
