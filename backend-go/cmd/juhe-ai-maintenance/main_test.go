package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestExecuteCommandDoesNotDuplicateStructuredPreflightFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.go"), []byte("package notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := &cobra.Command{Use: "test"}
	root.AddCommand(newMigrationCatalogPreflightCommand())
	root.SetArgs([]string{"migration-catalog-preflight", "--dir", dir})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	root.SetOut(&stdout)
	if code := executeCommand(root, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty after structured failure output", stderr.String())
	}
	var result commandPreflightResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not one structured JSON result: %v; output = %q", err, stdout.String())
	}
	if result.Success {
		t.Fatalf("result = %+v, want failure", result)
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
