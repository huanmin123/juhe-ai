package main

import (
	"io"
	"os"
	"strings"
	"sync"
	"testing"
)

// bdCaptureOutput 捕获 fn 期间写入 os.Stdout/os.Stderr 的内容（进程内替换，
// 本包测试不使用 t.Parallel()）。
func bdCaptureOutput(t *testing.T, fn func()) (string, string) {
	t.Helper()
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	savedStdout, savedStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdoutW, stderrW
	stdoutBuf, stderrBuf := &strings.Builder{}, &strings.Builder{}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(stdoutBuf, stdoutR) }()
	go func() { defer wg.Done(); _, _ = io.Copy(stderrBuf, stderrR) }()
	fn()
	os.Stdout, os.Stderr = savedStdout, savedStderr
	_ = stdoutW.Close()
	_ = stderrW.Close()
	wg.Wait()
	_ = stdoutR.Close()
	_ = stderrR.Close()
	return stdoutBuf.String(), stderrBuf.String()
}
