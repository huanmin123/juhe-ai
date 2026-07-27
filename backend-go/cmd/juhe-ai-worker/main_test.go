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

func TestWorkerCommandsUseCapabilityAwareRuntimeGate(t *testing.T) {
	var gated []string
	var ungated []string
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
		runWithoutOwnerGate: func(_ context.Context, runner app.WorkerRunner) error {
			if runner == nil {
				t.Fatal("ungated execution received nil runner")
			}
			ungated = append(ungated, "read-only")
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
		"model-quality-health-sync",
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

	quotaCommand, _, err := root.Find([]string{"gateway-quota-snapshot-build"})
	if err != nil {
		t.Fatalf("find gateway-quota-snapshot-build: %v", err)
	}
	quotaCommand.SetContext(t.Context())
	if err := quotaCommand.RunE(quotaCommand, nil); err != nil {
		t.Fatalf("run read-only gateway quota snapshot: %v", err)
	}
	if len(ungated) != 1 {
		t.Fatalf("read-only gateway quota snapshot ungated calls = %d, want 1", len(ungated))
	}
	beforeQuotaGate := len(gated)
	if err := quotaCommand.Flags().Set("publish-runtime-state", "true"); err != nil {
		t.Fatalf("set publish-runtime-state: %v", err)
	}
	if err := quotaCommand.RunE(quotaCommand, nil); err != nil {
		t.Fatalf("run publishing gateway quota snapshot: %v", err)
	}
	if len(gated) != beforeQuotaGate+1 || len(ungated) != 1 {
		t.Fatalf("publishing snapshot gate calls = %d, ungated = %d", len(gated)-beforeQuotaGate, len(ungated))
	}

	modelQualityCommand, _, err := root.Find([]string{"model-quality-health-sync"})
	if err != nil {
		t.Fatalf("find model-quality-health-sync: %v", err)
	}
	for _, flagName := range []string{
		"owner-id", "interval", "initial-delay", "batch-size", "workers", "lease", "attempt-timeout",
		"go-exclusive-owner", "legacy-worker-drained", "node-retention-safe", "run-once",
	} {
		if modelQualityCommand.Flags().Lookup(flagName) == nil {
			t.Fatalf("model-quality-health-sync flag %q is missing", flagName)
		}
	}

	versionCommand, _, err := root.Find([]string{"version"})
	if err != nil {
		t.Fatalf("find version: %v", err)
	}
	before := len(gated)
	beforeUngated := len(ungated)
	versionCommand.Run(versionCommand, nil)
	if len(gated) != before || len(ungated) != beforeUngated {
		t.Fatal("version command used worker execution gate")
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
