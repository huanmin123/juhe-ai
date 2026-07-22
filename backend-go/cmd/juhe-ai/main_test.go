package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestExecuteCommandWritesOneSanitizedFatalAndReturnsOne(t *testing.T) {
	const secret = "entry-secret"
	root := &cobra.Command{
		Use: "test",
		RunE: func(*cobra.Command, []string) error {
			return errors.New("failed\nAuthorization: Bearer " + secret + " api_key=" + secret)
		},
	}
	root.SetArgs([]string{})

	var stderr bytes.Buffer
	if code := executeCommand(root, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	assertFatalOnly(t, stderr.Bytes(), secret)
}

func TestExecuteCommandSuccessDoesNotWriteFatal(t *testing.T) {
	root := &cobra.Command{Use: "test", Run: func(*cobra.Command, []string) {}}
	root.SetArgs([]string{})

	var stderr bytes.Buffer
	if code := executeCommand(root, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestExecuteCommandReturnsWhenFatalWriterBlocks(t *testing.T) {
	root := &cobra.Command{Use: "test", RunE: func(*cobra.Command, []string) error { return errors.New("startup failed") }}
	root.SetArgs([]string{})
	writer := &blockingCommandWriter{started: make(chan struct{}), release: make(chan struct{})}
	defer close(writer.release)
	result := make(chan int, 1)
	go func() { result <- executeCommand(root, writer) }()
	select {
	case code := <-result:
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("executeCommand() did not return while stderr was blocked")
	}
}

func assertFatalOnly(t *testing.T, output []byte, secret string) {
	t.Helper()
	if bytes.Count(output, []byte{'\n'}) != 1 {
		t.Fatalf("stderr is not one line: %q", output)
	}
	if bytes.Contains(output, []byte(secret)) || strings.Contains(string(output), "Error:") || strings.Contains(string(output), "Usage:") {
		t.Fatalf("stderr leaked Cobra or secret output: %q", output)
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output), &record); err != nil {
		t.Fatalf("stderr is not valid JSON: %v; output = %q", err, output)
	}
	if record["level"] != "fatal" {
		t.Fatalf("fatal level = %#v, want fatal", record["level"])
	}
}

type blockingCommandWriter struct {
	started chan struct{}
	release chan struct{}
}

func (w *blockingCommandWriter) Write(data []byte) (int, error) {
	close(w.started)
	<-w.release
	return len(data), nil
}
