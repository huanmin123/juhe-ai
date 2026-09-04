package openaicompat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodeInterpreterExecutorGating(t *testing.T) {
	if CodeInterpreterExecutorForRequest(Config{}, nil, nil) != nil {
		t.Fatal("guidance mode -> nil executor")
	}
	executor := CodeInterpreterExecutorForRequest(Config{HostedToolCodeInterpreterMode: "local_runtime"}, newTestStore(t), nil)
	if executor == nil {
		t.Fatal("local_runtime -> executor")
	}
	if executor.config.PythonCommand != "python" || executor.config.TimeoutMs != 5000 {
		t.Fatalf("defaults = %+v", executor.config)
	}
	// Empty python command disables the executor (Node trims and checks).
	if CodeInterpreterExecutorForRequest(Config{
		HostedToolCodeInterpreterMode: "local_runtime",
		CodeInterpreter:               CodeInterpreterConfig{PythonCommand: "   "},
	}, nil, nil) != nil {
		t.Fatal("blank python command -> nil executor")
	}
}

func TestCodeInterpreterCodeTooLarge(t *testing.T) {
	executor := CodeInterpreterExecutorForRequest(Config{
		HostedToolCodeInterpreterMode: "local_runtime",
		CodeInterpreter:               CodeInterpreterConfig{MaxCodeBytes: 8},
	}, newTestStore(t), nil)
	result, err := executor.Execute(context.Background(), CodeInterpreterInput{Code: "123456789"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stderr != "Code interpreter input exceeds gateway limit (9 > 8 bytes)." {
		t.Fatalf("stderr = %q", result.Stderr)
	}
	if result.Metadata["error_code"] != "code_too_large" || result.Metadata["code_bytes"] != int64(9) || result.Metadata["max_code_bytes"] != int64(8) {
		t.Fatalf("metadata = %v", result.Metadata)
	}
}

func TestCodeInterpreterRunWithFakeRunner(t *testing.T) {
	store := newTestStore(t)
	executor := CodeInterpreterExecutorForRequest(Config{
		HostedToolCodeInterpreterMode: "local_runtime",
		FilesRoot:                     t.TempDir(),
	}, store, &CodeInterpreterScope{SystemAccountID: testScopeA, APIKeyID: testKeyA})
	observed := map[string]string{}
	executor.runner = func(ctx context.Context, config CodeInterpreterConfig, runnerPath, codePath, workDir string) interpreterRunResult {
		observed["code"] = mustRead(t, codePath)
		observed["runner"] = mustRead(t, runnerPath)
		observed["dir"] = filepath.Base(workDir)
		// Drop an artifact into the work dir before returning.
		if err := os.WriteFile(filepath.Join(workDir, "result.csv"), []byte("a,b"), 0o644); err != nil {
			t.Fatal(err)
		}
		stdout := "printed"
		code := 0
		return interpreterRunResult{Stdout: stdout, ExitCode: &code}
	}
	result, err := executor.Execute(context.Background(), CodeInterpreterInput{Code: "print('hi')"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "printed" || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("result = %+v", result)
	}
	if observed["code"] != "print('hi')" {
		t.Fatalf("code file = %q", observed["code"])
	}
	if !strings.Contains(observed["runner"], "Network and subprocess access are disabled") {
		t.Fatalf("runner source mismatch")
	}
	if !strings.HasPrefix(observed["dir"], "ci-") {
		t.Fatalf("work dir prefix = %s", observed["dir"])
	}
	// Artifacts persisted with the container + download paths.
	if len(result.Artifacts) != 1 {
		t.Fatalf("artifacts = %+v", result.Artifacts)
	}
	artifact := result.Artifacts[0]
	if artifact.Filename != "result.csv" || artifact.FileID == "" || artifact.DownloadPath != "/v1/files/"+artifact.FileID+"/content" {
		t.Fatalf("artifact = %+v", artifact)
	}
	if result.ArtifactsTotalBytes != 3 || result.ArtifactsOmittedCount != 0 {
		t.Fatalf("artifact totals = %+v", result)
	}
	if result.Metadata["artifacts_scanned_entries"] != 3 { // input.py, runner.py, result.csv
		t.Fatalf("scanned entries = %v", result.Metadata)
	}
}

func TestCodeInterpreterSpawnFailure(t *testing.T) {
	executor := CodeInterpreterExecutorForRequest(Config{
		HostedToolCodeInterpreterMode: "local_runtime",
		FilesRoot:                     t.TempDir(),
	}, newTestStore(t), nil)
	executor.runner = func(ctx context.Context, config CodeInterpreterConfig, runnerPath, codePath, workDir string) interpreterRunResult {
		return interpreterRunResult{SpawnErr: errors.New("exec: no such binary")}
	}
	result, err := executor.Execute(context.Background(), CodeInterpreterInput{Code: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stderr != "Code interpreter python process failed: exec: no such binary" || result.ExitCode == nil || *result.ExitCode != 1 {
		t.Fatalf("result = %+v", result)
	}
	if result.Metadata["error_code"] != "python_spawn_failed" {
		t.Fatalf("metadata = %v", result.Metadata)
	}
}

func TestCodeInterpreterArtifactsBoundaries(t *testing.T) {
	store := newTestStore(t)
	root := t.TempDir()
	executor := CodeInterpreterExecutorForRequest(Config{
		HostedToolCodeInterpreterMode: "local_runtime",
		FilesRoot:                     root,
		CodeInterpreter: CodeInterpreterConfig{
			MaxArtifactCount: 2,
			MaxArtifactBytes: 4,
		},
	}, store, nil) // no scope -> missing_gateway_scope for persistable files
	executor.config.TempRoot = t.TempDir()
	executor.runner = func(ctx context.Context, config CodeInterpreterConfig, runnerPath, codePath, workDir string) interpreterRunResult {
		// Nested dir + symlink + oversize + small files.
		if err := os.MkdirAll(filepath.Join(workDir, "nested"), 0o755); err != nil {
			t.Fatal(err)
		}
		write := func(name, content string) {
			if err := os.WriteFile(filepath.Join(workDir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write("nested/deep.txt", "deep")
		write("big.bin", "toolarge")
		write("small.txt", "ok")
		if err := os.Symlink(filepath.Join(workDir, "small.txt"), filepath.Join(workDir, "link.txt")); err != nil {
			t.Log(err) // Windows may lack symlink privilege; skip silently.
		}
		return interpreterRunResult{}
	}
	result, err := executor.Execute(context.Background(), CodeInterpreterInput{Code: "pass"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Artifacts) != 2 {
		t.Fatalf("artifacts = %+v", result.Artifacts)
	}
	// small.txt (3 bytes) fits; big.bin is content-omitted file_too_large.
	byName := map[string]CodeInterpreterArtifact{}
	for _, artifact := range result.Artifacts {
		byName[artifact.Filename] = artifact
	}
	if artifact := byName["big.bin"]; artifact.OmitReason != "file_too_large" || !artifact.ContentOmitted {
		t.Fatalf("big.bin = %+v", artifact)
	}
	if artifact := byName["nested/deep.txt"]; artifact.OmitReason != "missing_gateway_scope" {
		t.Fatalf("nested artifact = %+v", artifact)
	}
	// Max 2 artifacts: the newest (small.txt) was popped and counted omitted,
	// mirroring Node collector.artifacts.pop().
	if _, present := byName["small.txt"]; present {
		t.Fatalf("small.txt should have been popped: %+v", result.Artifacts)
	}
	if result.ArtifactsTotalBytes != int64(len("deep")+len("toolarge")+len("ok")) {
		t.Fatalf("total bytes = %d", result.ArtifactsTotalBytes)
	}
	if result.ArtifactsOmittedCount == 0 {
		t.Fatalf("expected omitted count (link or popped artifact), got %+v", result)
	}
}

func TestCodeInterpreterArtifactPersistPath(t *testing.T) {
	store := newTestStore(t)
	root := t.TempDir()
	executor := CodeInterpreterExecutorForRequest(Config{
		HostedToolCodeInterpreterMode: "local_runtime",
		FilesRoot:                     root,
	}, store, &CodeInterpreterScope{SystemAccountID: testScopeA, APIKeyID: testKeyA})
	executor.config.TempRoot = t.TempDir()
	executor.runner = func(ctx context.Context, config CodeInterpreterConfig, runnerPath, codePath, workDir string) interpreterRunResult {
		if err := os.WriteFile(filepath.Join(workDir, "chart.png"), []byte("png-bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
		return interpreterRunResult{}
	}
	result, err := executor.Execute(context.Background(), CodeInterpreterInput{Code: "pass", ContainerID: "container-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Artifacts) != 1 {
		t.Fatalf("artifacts = %+v", result.Artifacts)
	}
	artifact := result.Artifacts[0]
	if artifact.OmitReason != "" || artifact.ContentOmitted {
		t.Fatalf("artifact = %+v", artifact)
	}
	if artifact.MediaType != "image/png" || artifact.ContainerID != "container-1" {
		t.Fatalf("artifact = %+v", artifact)
	}
	if artifact.ContainerDownloadPath != "/v1/containers/container-1/files/"+artifact.FileID+"/content" {
		t.Fatalf("container path = %s", artifact.ContainerDownloadPath)
	}
	// DB record created with the code_interpreter_output purpose.
	record, err := store.FindFile(context.Background(), artifact.FileID, testScopeA, testKeyA)
	if err != nil || record == nil || record.Purpose != "code_interpreter_output" {
		t.Fatalf("record = %+v %v", record, err)
	}
	if record.ContainerID == nil || *record.ContainerID != "container-1" {
		t.Fatalf("container binding = %+v", record)
	}
	onDisk, err := os.ReadFile(filepath.Join(root, record.StorageKey))
	if err != nil || string(onDisk) != "png-bytes" {
		t.Fatalf("stored object = %v %q", err, onDisk)
	}
}

func TestCodeInterpreterTempDirCleanup(t *testing.T) {
	store := newTestStore(t)
	tempRoot := t.TempDir()
	executor := CodeInterpreterExecutorForRequest(Config{
		HostedToolCodeInterpreterMode: "local_runtime",
		FilesRoot:                     t.TempDir(),
		CodeInterpreter:               CodeInterpreterConfig{TempRoot: tempRoot, CleanupTempDirectory: true},
	}, store, nil)
	executor.runner = func(ctx context.Context, config CodeInterpreterConfig, runnerPath, codePath, workDir string) interpreterRunResult {
		return interpreterRunResult{}
	}
	if _, err := executor.Execute(context.Background(), CodeInterpreterInput{Code: "pass"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(tempRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temp root not cleaned: %d entries", len(entries))
	}
}

func TestCodeInterpreterAbortMetadata(t *testing.T) {
	store := newTestStore(t)
	executor := CodeInterpreterExecutorForRequest(Config{
		HostedToolCodeInterpreterMode: "local_runtime",
		FilesRoot:                     t.TempDir(),
	}, store, nil)
	executor.runner = func(ctx context.Context, config CodeInterpreterConfig, runnerPath, codePath, workDir string) interpreterRunResult {
		return interpreterRunResult{Aborted: true, TimedOut: false}
	}
	result, err := executor.Execute(context.Background(), CodeInterpreterInput{Code: "pass"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Metadata["aborted"] != true {
		t.Fatalf("metadata = %v", result.Metadata)
	}
}

func TestNormalizeCodeInterpreterArtifactPath(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"a\\b.txt", "a/b.txt"},
		{"a//b.txt", "a/b.txt"},
		{"./a.txt", "a.txt"},
		{"a\x01b", "a_b"},
		{"", "artifact"},
	}
	for _, tc := range tests {
		if got := normalizeArtifactPath(tc.in); got != tc.want {
			t.Fatalf("normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
