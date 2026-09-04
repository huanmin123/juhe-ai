package openaicompat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CodeInterpreter ports openai-compatible-code-interpreter/code-interpreter-executor.ts:
// a sandboxed local python runner plus artifact persistence into the
// openai-compatible file store (purpose code_interpreter_output).

// CodeInterpreterScope mirrors CodeInterpreterGatewayScope.
type CodeInterpreterScope struct {
	SystemAccountID string
	APIKeyID        string
}

// CodeInterpreterArtifact mirrors OpenAIToAnthropicCodeInterpreterArtifact.
type CodeInterpreterArtifact struct {
	Filename              string
	Bytes                 int64
	FileID                string
	DownloadPath          string
	ContainerID           string
	ContainerDownloadPath string
	MediaType             string
	ContentOmitted        bool
	OmitReason            string
}

// CodeInterpreterExecutionResult mirrors
// OpenAIToAnthropicCodeInterpreterExecutionResult.
type CodeInterpreterExecutionResult struct {
	Stdout                string
	Stderr                string
	ExitCode              *int
	TimedOut              bool
	OutputTruncated       bool
	Artifacts             []CodeInterpreterArtifact
	ArtifactsOmittedCount int
	ArtifactsTotalBytes   int64
	Metadata              map[string]any
}

// CodeInterpreterInput mirrors OpenAIToAnthropicCodeInterpreterRuntimeInput.
type CodeInterpreterInput struct {
	Code        string
	Tool        map[string]any
	ContainerID string
}

// CodeInterpreterExecutor mirrors OpenAIToAnthropicCodeInterpreterExecutor.
type CodeInterpreterExecutor struct {
	config CodeInterpreterConfig
	scope  *CodeInterpreterScope
	store  *Store
	root   string

	// runner is the process seam (mock-first testing). The default runs the
	// real python subprocess; tests inject a fake.
	runner interpreterRunnerFunc

	// now stamps artifact creation.
	now func() time.Time
}

// interpreterRunResult carries the runner outcome (mirrors the promise
// resolution of spawnPythonCodeInterpreter).
type interpreterRunResult struct {
	Stdout          string
	Stderr          string
	ExitCode        *int
	TimedOut        bool
	OutputTruncated bool
	Aborted         bool
	SpawnErr        error
}

type interpreterRunnerFunc func(ctx context.Context, config CodeInterpreterConfig, runnerPath, codePath, workDir string) interpreterRunResult

// CodeInterpreterExecutorForRequest mirrors
// openAICompatibleCodeInterpreterExecutorForGatewayRequest: nil unless the
// hosted tool runtime is local_runtime and a python command is configured.
func CodeInterpreterExecutorForRequest(config Config, store *Store, scope *CodeInterpreterScope) *CodeInterpreterExecutor {
	config = config.withDefaults()
	if config.HostedToolCodeInterpreterMode != "local_runtime" {
		return nil
	}
	if strings.TrimSpace(config.CodeInterpreter.PythonCommand) == "" {
		return nil
	}
	return newCodeInterpreterExecutor(config, store, scope)
}

func newCodeInterpreterExecutor(config Config, store *Store, scope *CodeInterpreterScope) *CodeInterpreterExecutor {
	return &CodeInterpreterExecutor{
		config: config.withDefaults().CodeInterpreter,
		scope:  scope,
		store:  store,
		root:   config.withDefaults().FilesRoot,
		runner: runPythonProcess,
		now:    time.Now,
	}
}

// Execute mirrors executeOpenAICompatibleCodeInterpreter.
func (e *CodeInterpreterExecutor) Execute(ctx context.Context, input CodeInterpreterInput) (*CodeInterpreterExecutionResult, error) {
	codeBytes := int64(len(input.Code))
	if codeBytes > e.config.MaxCodeBytes {
		return &CodeInterpreterExecutionResult{
			Stderr: fmt.Sprintf("Code interpreter input exceeds gateway limit (%d > %d bytes).", codeBytes, e.config.MaxCodeBytes),
			Metadata: map[string]any{
				"error_code":     "code_too_large",
				"code_bytes":     codeBytes,
				"max_code_bytes": e.config.MaxCodeBytes,
			},
		}, nil
	}
	if err := os.MkdirAll(e.config.TempRoot, 0o755); err != nil {
		return nil, err
	}
	workDir, err := os.MkdirTemp(e.config.TempRoot, "ci-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if e.config.CleanupTempDirectory {
			_ = os.RemoveAll(workDir)
		}
	}()
	codePath := filepath.Join(workDir, "input.py")
	runnerPath := filepath.Join(workDir, "runner.py")
	if err := os.WriteFile(codePath, []byte(input.Code), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(runnerPath, []byte(codeInterpreterRunnerSource()), 0o644); err != nil {
		return nil, err
	}
	run := e.runner(ctx, e.config, runnerPath, codePath, workDir)
	result := &CodeInterpreterExecutionResult{
		Stdout:          run.Stdout,
		Stderr:          run.Stderr,
		ExitCode:        run.ExitCode,
		TimedOut:        run.TimedOut,
		OutputTruncated: run.OutputTruncated,
	}
	if run.Aborted {
		result.Metadata = map[string]any{"aborted": true}
	}
	if run.SpawnErr != nil {
		result.Stdout = run.Stdout
		result.Stderr = fmt.Sprintf("Code interpreter python process failed: %s", run.SpawnErr.Error())
		result.ExitCode = intPtr(1)
		result.Metadata = map[string]any{"error_code": "python_spawn_failed"}
	}
	artifacts, err := e.collectArtifacts(workDir, input.ContainerID)
	// Node swallows collection errors into artifacts_scan_failed.
	if err != nil {
		result.Metadata = mergeMetadata(result.Metadata, map[string]any{
			"artifacts_scanned_entries": 0,
			"artifacts_scan_failed":     true,
		})
		return result, nil
	}
	result.Artifacts = artifacts.artifacts
	result.ArtifactsOmittedCount = artifacts.omittedCount
	result.ArtifactsTotalBytes = artifacts.totalBytes
	result.Metadata = mergeMetadata(result.Metadata, artifacts.metadata)
	return result, nil
}

type artifactCollection struct {
	artifacts    []CodeInterpreterArtifact
	omittedCount int
	totalBytes   int64
	metadata     map[string]any
}

// collectArtifacts mirrors collectCodeInterpreterArtifacts +
// collectCodeInterpreterArtifactsFromDirectory.
func (e *CodeInterpreterExecutor) collectArtifacts(workDir, containerID string) (*artifactCollection, error) {
	collection := &artifactCollection{
		artifacts: []CodeInterpreterArtifact{},
		metadata:  map[string]any{"artifacts_scanned_entries": 0},
	}
	err := e.collectFromDirectory(workDir, "", containerID, collection)
	if err != nil {
		collection.metadata["artifacts_scan_failed"] = true
	}
	return collection, nil
}

func (e *CodeInterpreterExecutor) collectFromDirectory(workDir, relativeDir, containerID string, collection *artifactCollection) error {
	absoluteDir := workDir
	if relativeDir != "" {
		absoluteDir = filepath.Join(workDir, relativeDir)
	}
	entries, err := os.ReadDir(absoluteDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		collection.metadata["artifacts_scanned_entries"] = collection.metadata["artifacts_scanned_entries"].(int) + 1
		entryRelativePath := normalizeArtifactPath(entry.Name())
		if relativeDir != "" {
			entryRelativePath = normalizeArtifactPath(relativeDir + "/" + entry.Name())
		}
		if relativeDir == "" && (entry.Name() == "input.py" || entry.Name() == "runner.py") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			collection.omittedCount++
			continue
		}
		if entry.IsDir() {
			if err := e.collectFromDirectory(workDir, entryRelativePath, containerID, collection); err != nil {
				return err
			}
			continue
		}
		if !entry.Type().IsRegular() {
			collection.omittedCount++
			continue
		}
		info, statErr := os.Lstat(filepath.Join(absoluteDir, entry.Name()))
		if statErr != nil {
			collection.omittedCount++
			continue
		}
		collection.totalBytes += info.Size()
		artifact := e.artifactFromFile(filepath.Join(absoluteDir, entry.Name()), entryRelativePath, info.Size(), containerID)
		collection.artifacts = append(collection.artifacts, artifact)
		if len(collection.artifacts) > e.config.MaxArtifactCount {
			collection.artifacts = collection.artifacts[:len(collection.artifacts)-1]
			collection.omittedCount++
		}
	}
	return nil
}

// artifactFromFile mirrors codeInterpreterArtifactFromFile.
func (e *CodeInterpreterExecutor) artifactFromFile(absolutePath, filename string, size int64, containerID string) CodeInterpreterArtifact {
	mediaType := MediaTypeFromFilename(filename)
	if size > e.config.MaxArtifactBytes {
		return CodeInterpreterArtifact{
			Filename: filename, Bytes: size, MediaType: mediaType, ContainerID: containerID,
			ContentOmitted: true, OmitReason: "file_too_large",
		}
	}
	if e.scope == nil {
		return CodeInterpreterArtifact{
			Filename: filename, Bytes: size, MediaType: mediaType, ContainerID: containerID,
			ContentOmitted: true, OmitReason: "missing_gateway_scope",
		}
	}
	fileID, persistErr := e.persistArtifactFile(absolutePath, filename, size, mediaType, containerID)
	if persistErr != nil {
		return CodeInterpreterArtifact{
			Filename: filename, Bytes: size, MediaType: mediaType, ContainerID: containerID,
			ContentOmitted: true, OmitReason: "persist_failed",
		}
	}
	artifact := CodeInterpreterArtifact{
		Filename:     filename,
		Bytes:        size,
		FileID:       fileID,
		DownloadPath: "/v1/files/" + fileID + "/content",
		ContainerID:  containerID,
		MediaType:    mediaType,
	}
	if containerID != "" {
		artifact.ContainerDownloadPath = "/v1/containers/" + containerID + "/files/" + fileID + "/content"
	}
	return artifact
}

// persistArtifactFile mirrors persistCodeInterpreterArtifactFile: copy with
// sha256, persist the DB record, remove the object when the DB write fails.
func (e *CodeInterpreterExecutor) persistArtifactFile(sourcePath, filename string, size int64, mediaType, containerID string) (string, error) {
	fileID := e.store.generateID("file")
	storageKey := StorageKeyForFile(fileID)
	targetPath, err := EnsureFileObjectParent(e.root, storageKey)
	if err != nil {
		return "", err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return "", err
	}
	defer source.Close()
	target, err := os.Create(targetPath)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(target, hash), source)
	closeErr := target.Close()
	if copyErr != nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = RemoveFileObject(e.root, storageKey)
		return "", copyErr
	}
	var containerPtr *string
	if containerID != "" {
		containerPtr = &containerID
	}
	var mediaTypePtr *string
	if mediaType != "" {
		mediaTypePtr = &mediaType
	}
	_, err = e.store.CreateFile(context.Background(), FileCreateInput{
		ID:              fileID,
		SystemAccountID: e.scope.SystemAccountID,
		APIKeyID:        e.scope.APIKeyID,
		Purpose:         "code_interpreter_output",
		ContainerID:     containerPtr,
		Filename:        filename,
		Bytes:           size,
		MediaType:       mediaTypePtr,
		StorageKey:      storageKey,
		SHA256:          hex.EncodeToString(hash.Sum(nil)),
	})
	if err != nil {
		_ = RemoveFileObject(e.root, storageKey)
		return "", err
	}
	return fileID, nil
}

// normalizeArtifactPath mirrors normalizeCodeInterpreterArtifactPath.
func normalizeArtifactPath(value string) string {
	normalized := strings.ReplaceAll(value, "\\", "/")
	for strings.Contains(normalized, "//") {
		normalized = strings.ReplaceAll(normalized, "//", "/")
	}
	normalized = strings.TrimPrefix(normalized, "./")
	var out strings.Builder
	for _, symbol := range normalized {
		if symbol <= 0x1f || symbol == 0x7f {
			out.WriteRune('_')
			continue
		}
		out.WriteRune(symbol)
	}
	result := []rune(out.String())
	if len(result) > 512 {
		result = result[:512]
	}
	normalized = string(result)
	if normalized == "" {
		return "artifact"
	}
	return normalized
}

// runPythonProcess is the default interpreterRunnerFunc mirroring
// spawnPythonCodeInterpreter: -I -B, whitelisted env, shared output byte cap,
// timeout kill and abort handling.
func runPythonProcess(ctx context.Context, config CodeInterpreterConfig, runnerPath, codePath, workDir string) interpreterRunResult {
	result := interpreterRunResult{}
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(runCtx, config.PythonCommand, "-I", "-B", runnerPath, codePath)
	command.Dir = workDir
	command.Env = codeInterpreterProcessEnv()
	stdoutPipe, err := command.StdoutPipe()
	if err != nil {
		result.SpawnErr = err
		return result
	}
	stderrPipe, err := command.StderrPipe()
	if err != nil {
		result.SpawnErr = err
		return result
	}
	if err := command.Start(); err != nil {
		result.SpawnErr = err
		return result
	}
	timedOut := false
	timer := time.AfterFunc(time.Duration(config.TimeoutMs)*time.Millisecond, func() {
		timedOut = true
		cancel()
	})
	defer timer.Stop()

	collect := func(reader io.Reader) (string, bool) {
		remaining := config.MaxOutputBytes
		truncated := false
		var out bytes.Buffer
		buffer := make([]byte, 8192)
		for {
			n, readErr := reader.Read(buffer)
			if n > 0 {
				if remaining <= 0 {
					truncated = true
					cancel()
					continue
				}
				take := int64(n)
				if take > remaining {
					take = remaining
					truncated = true
				}
				out.Write(buffer[:take])
				remaining -= take
				if take < int64(n) {
					cancel()
				}
			}
			if readErr != nil {
				break
			}
		}
		return out.String(), truncated
	}
	stdoutText, stdoutTruncated := collect(stdoutPipe)
	stderrText, stderrTruncated := collect(stderrPipe)
	waitErr := command.Wait()
	result.Stdout = stdoutText
	result.Stderr = stderrText
	result.TimedOut = timedOut
	result.OutputTruncated = stdoutTruncated || stderrTruncated
	if ctx.Err() != nil && !timedOut {
		result.Aborted = true
	}
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			code := exitErr.ExitCode()
			result.ExitCode = &code
		} else if runCtx.Err() != nil {
			// killed by timeout/abort: Node resolves with kill() and close(null).
		} else {
			result.SpawnErr = waitErr
		}
	}
	return result
}

// codeInterpreterProcessEnv mirrors codeInterpreterProcessEnv.
func codeInterpreterProcessEnv() []string {
	env := map[string]string{
		"PYTHONIOENCODING": "utf-8",
		"PYTHONUTF8":       "1",
		"NO_PROXY":         "*",
	}
	for _, key := range []string{"PATH", "Path", "SystemRoot", "WINDIR", "TEMP", "TMP", "PATHEXT"} {
		if value := os.Getenv(key); value != "" {
			env[key] = value
		}
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+env[key])
	}
	return pairs
}

// codeInterpreterRunnerSource mirrors codeInterpreterRunnerSource verbatim
// (the sandbox declaration block disabling network/subprocess access).
func codeInterpreterRunnerSource() string {
	return `
import os
import socket
import subprocess
import sys
import traceback

def _blocked(*args, **kwargs):
    raise RuntimeError("Network and subprocess access are disabled in this code interpreter runtime")

socket.socket = _blocked
socket.create_connection = _blocked
subprocess.Popen = _blocked
subprocess.run = _blocked
subprocess.call = _blocked
subprocess.check_call = _blocked
subprocess.check_output = _blocked
os.system = _blocked
os.popen = _blocked

code_path = sys.argv[1]
globals_dict = {"__name__": "__main__", "__file__": code_path}

try:
    with open(code_path, "r", encoding="utf-8") as handle:
        source = handle.read()
    exec(compile(source, code_path, "exec"), globals_dict)
except SystemExit:
    raise
except BaseException:
    traceback.print_exc()
    sys.exit(1)
`
}

func mergeMetadata(base, extra map[string]any) map[string]any {
	merged := map[string]any{}
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range extra {
		merged[key] = value
	}
	return merged
}
