//go:build windows

package acceptance

import (
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
)

// windowsNewProcessGroupAttr 让子进程进入独立进程组，CTRL_BREAK 只投递给
// 该子进程（Go runtime 将 CTRL_BREAK_EVENT 映射为 os.Interrupt，触发
// supervisor 的 signal.NotifyContext 干净停机路径）。
func windowsNewProcessGroupAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

// interruptProcess 在 Windows 上发送 CTRL_BREAK_EVENT 实现 SIGTERM 等价的
// 优雅停机信号。
func interruptProcess(cmd *exec.Cmd) error {
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(cmd.Process.Pid))
}

var _ = strconv.Itoa
