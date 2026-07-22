package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"juhe-ai/backend-go/internal/app"
	"juhe-ai/backend-go/internal/config"
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

func TestSevenWorkerCommandsUseSharedRuntimeGate(t *testing.T) {
	var gated []string
	deps := workerCommandDependencies{
		loadConfig: func() (config.Config, error) { return config.Config{}, nil },
		newLogger: func(string, io.Writer) (*slog.Logger, error) {
			return slog.New(slog.NewTextHandler(io.Discard, nil)), nil
		},
		runWithRuntimeGate: func(_ context.Context, _ config.Config, _ *slog.Logger, runner app.WorkerRunner) error {
			if runner == nil {
				t.Fatal("runtime gate received nil runner")
			}
			gated = append(gated, "gate")
			return nil
		},
	}
	root := newRootCommand(deps)
	for _, name := range []string{
		"ingest",
		"account-test",
		"authorization-expiry-sweep",
		"operation-log-retention-cleanup",
		"authorization-usage-range-windows-refresh",
		"gateway-quota-snapshot-build",
		"cooldown-account-retest",
	} {
		command, _, err := root.Find([]string{name})
		if err != nil {
			t.Fatalf("find %s: %v", name, err)
		}
		if command.RunE == nil {
			t.Fatalf("%s RunE is nil", name)
		}
		command.SetContext(t.Context())
		before := len(gated)
		if err := command.RunE(command, nil); err != nil {
			t.Fatalf("run %s: %v", name, err)
		}
		if len(gated) != before+1 {
			t.Fatalf("%s runtime gate calls = %d, want one additional call", name, len(gated)-before)
		}
	}

	versionCommand, _, err := root.Find([]string{"version"})
	if err != nil {
		t.Fatalf("find version: %v", err)
	}
	before := len(gated)
	versionCommand.Run(versionCommand, nil)
	if len(gated) != before {
		t.Fatal("version command used worker runtime gate")
	}
}

func TestCooldownAccountRetestCommandStaysDefaultOffWhileNodeOwnsWorker(t *testing.T) {
	field, ok := reflect.TypeOf(config.Config{}).FieldByName("CooldownAccountRetestWorkerEnabled")
	if !ok || field.Tag.Get("envDefault") != "false" {
		t.Fatalf("CooldownAccountRetestWorkerEnabled field=%+v ok=%v", field, ok)
	}
	manifestPath := filepath.Clean(filepath.Join("..", "..", "..", "deploy", "owner-manifest.json"))
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read owner manifest: %v", err)
	}
	var manifest struct {
		RouteOwners map[string]string `json:"routeOwners"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse owner manifest: %v", err)
	}
	if manifest.RouteOwners["worker"] != "node" {
		t.Fatalf("worker owner = %q, want node", manifest.RouteOwners["worker"])
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
