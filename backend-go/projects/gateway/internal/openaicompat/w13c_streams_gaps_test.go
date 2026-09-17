package openaicompat

// w13c 覆盖率补充测试（流式桥与执行器族）：Gemini 流状态机、图像生成 SSE
// 迭代器错误形态、runPythonProcess 进程边界与文件存储/索引小分支。

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestW13CGeminiChatStreamArms(t *testing.T) {
	// 已完成/已失败状态短路。
	state := &GeminiChatStreamState{}
	state.Completed = true
	if got := FailGeminiChatStream(state, "m", ""); got != nil {
		t.Fatal("completed state must not fail again")
	}
	state.Failed = true
	state.Completed = false
	if got := ProcessChatCompletionsSseEventAsGemini(state, "data: {}"); got != nil {
		t.Fatal("failed state must ignore events")
	}

	// [DONE] 与解析失败。
	fresh := &GeminiChatStreamState{Model: "m"}
	if got := ProcessChatCompletionsSseEventAsGemini(fresh, "data: [DONE]"); len(got) == 0 {
		t.Fatal("[DONE] must complete the stream")
	}
	parseFail := &GeminiChatStreamState{}
	if got := ProcessChatCompletionsSseEventAsGemini(parseFail, "data: {bad"); len(got) != 1 {
		t.Fatalf("parse error must fail the stream: %v", got)
	}

	// 错误事件的三种形态。
	errorEvent := &GeminiChatStreamState{}
	got := ProcessChatCompletionsSseEventAsGemini(errorEvent,
		"event: error\ndata: {\"error\":{\"message\":\"失败\",\"code\":\"bad\"}}")
	if len(got) != 1 || !strings.Contains(got[0], "失败") {
		t.Fatalf("error event = %v", got)
	}
	responseFailed := &GeminiChatStreamState{}
	got = ProcessChatCompletionsSseEventAsGemini(responseFailed,
		"data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"type\":\"upstream\"}}}")
	if len(got) != 1 || !strings.Contains(got[0], "upstream") {
		t.Fatalf("response.failed = %v", got)
	}
	bareError := &GeminiChatStreamState{}
	got = ProcessChatCompletionsSseEventAsGemini(bareError, "event: error\ndata: {\"oops\":1}")
	if len(got) != 1 || !strings.Contains(got[0], "上游 Chat Completions SSE 返回错误事件") {
		t.Fatalf("bare error data = %v", got)
	}

	// 非 choice 事件（无 delta）保持静默。
	quiet := &GeminiChatStreamState{}
	if got := ProcessChatCompletionsSseEventAsGemini(quiet, "data: {\"model\":\"m2\"}"); len(got) != 0 {
		t.Fatalf("model-only event = %v", got)
	}

	// 完成事件带 usage。
	withUsage := &GeminiChatStreamState{}
	withUsage.Usage = map[string]any{"totalTokenCount": 3}
	completed := CompleteGeminiChatStream(withUsage)
	if len(completed) == 0 || !strings.Contains(completed[len(completed)-1], "usageMetadata") {
		t.Fatalf("usage completion = %v", completed)
	}
}

func TestW13CImageGenerationStreamArms(t *testing.T) {
	// 响应体超过读取上限。
	executor := covImageExecutor(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", 4096))),
		}, nil
	})
	executor.Provider.MaxBodyBytes = 16
	iterator, err := executor.GenerateStream(context.Background(), ImageGenerationInput{Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := iterator(); err == nil || !strings.Contains(err.Error(), "读取上限") {
		t.Fatalf("oversized stream = %v", err)
	}

	// 事件帧解析失败。
	executor = covImageExecutor(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"image_generation.partial_image\"}\n\n")),
		}, nil
	})
	iterator, err = executor.GenerateStream(context.Background(), ImageGenerationInput{Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := iterator(); err == nil || !strings.Contains(err.Error(), "b64_json") {
		t.Fatalf("bad frame = %v", err)
	}

	// 上下文取消。
	executor = covImageExecutor(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	iterator, err = executor.GenerateStream(ctx, ImageGenerationInput{Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := iterator(); err == nil {
		t.Fatal("canceled context must surface")
	}

	// readResponseTextWithLimit：空 body / 超限 / 读错误。
	if text, err := readResponseTextWithLimit(&http.Response{}, 10); err != nil || text != "" {
		t.Fatalf("nil body = %q %v", text, err)
	}
	oversized := &http.Response{Body: io.NopCloser(strings.NewReader("12345678901"))}
	if _, err := readResponseTextWithLimit(oversized, 10); err == nil || !strings.Contains(err.Error(), "读取上限") {
		t.Fatalf("oversized body = %v", err)
	}
	failing := &http.Response{Body: io.NopCloser(w13cErrReader{})}
	if _, err := readResponseTextWithLimit(failing, 10); err == nil {
		t.Fatal("read failure must surface")
	}
	// discardResponse 对 nil body 安全。
	discardResponse(nil)
	discardResponse(&http.Response{})

	// imageGenerationDataItem 非对象形态。
	if imageGenerationDataItem("not-a-map") != nil {
		t.Fatal("non-map must yield nil")
	}
	if imageGenerationDataItem(map[string]any{"data": []any{}}) != nil {
		t.Fatal("empty data must yield nil")
	}
}

type w13cErrReader struct{}

func (w13cErrReader) Read([]byte) (int, error) { return 0, errors.New("w13c read failure") }

func TestW13CRunPythonProcessArms(t *testing.T) {
	pythonPath, err := exec.LookPath("python")
	if err != nil {
		pythonPath, err = exec.LookPath("python3")
		if err != nil {
			t.Skip("未安装 python 解释器；跳过 runPythonProcess 真实进程用例")
		}
	}
	workDir := t.TempDir()
	config := CodeInterpreterConfig{
		PythonCommand:  pythonPath,
		TimeoutMs:      15000,
		MaxOutputBytes: 1 << 20,
	}

	// 启动失败：不存在的解释器。
	spawnFail := config
	spawnFail.PythonCommand = "w13c-nonexistent-python"
	runner := filepath.Join(workDir, "runner.py")
	code := filepath.Join(workDir, "code.py")
	if err := os.WriteFile(runner, []byte("import sys; exec(open(sys.argv[1]).read())"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(code, []byte("print('ok')"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := runPythonProcess(context.Background(), spawnFail, runner, code, workDir)
	if result.SpawnErr == nil {
		t.Fatal("missing interpreter must fail to spawn")
	}

	// 非零退出码。
	exitResult := runPythonProcess(context.Background(), config, runner, writeW13CFile(t, workDir, "exit.py", "import sys; sys.exit(3)"), workDir)
	if exitResult.ExitCode == nil || *exitResult.ExitCode != 3 {
		t.Fatalf("exit code = %v", exitResult.ExitCode)
	}

	// 输出超限截断。
	truncatedConfig := config
	truncatedConfig.MaxOutputBytes = 16
	truncated := runPythonProcess(context.Background(), truncatedConfig, runner,
		writeW13CFile(t, workDir, "flood.py", "print('x' * 4096)"), workDir)
	if !truncated.OutputTruncated {
		t.Fatalf("flooded output must truncate: %+v", truncated)
	}

	// 超时 kill。
	timeoutConfig := config
	timeoutConfig.TimeoutMs = 150
	timedOut := runPythonProcess(context.Background(), timeoutConfig, runner,
		writeW13CFile(t, workDir, "sleep.py", "import time; time.sleep(5)"), workDir)
	if !timedOut.TimedOut {
		t.Fatalf("long sleep must time out: %+v", timedOut)
	}

	// 外部取消 → abort。
	abortCtx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	aborted := runPythonProcess(abortCtx, config, runner,
		writeW13CFile(t, workDir, "sleep2.py", "import time; time.sleep(5)"), workDir)
	if !aborted.Aborted {
		t.Fatalf("canceled context must abort: %+v", aborted)
	}
}

func writeW13CFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestW13CFileStorageAndIndexerArms(t *testing.T) {
	root := t.TempDir()
	// 存储键越界拒绝。
	if _, err := FileObjectPath(root, "../escape"); err == nil {
		t.Fatal("escape key must be rejected")
	}
	if _, err := FileObjectPath(root, "/abs"); err == nil {
		t.Fatal("root-anchored key must be rejected")
	}
	if err := RemoveFileObject(root, "../escape"); err == nil {
		t.Fatal("escape removal must be rejected")
	}
	// 不存在文件的移除非错误。
	if err := RemoveFileObject(root, "files/ab/absent.txt"); err != nil {
		t.Fatalf("absent removal = %v", err)
	}
	// 文本索引：不支持的媒体类型 / 空文本 / 字节超限。
	if _, err := BuildVectorStoreChunks(root, FileRecord{ID: "f", StorageKey: "files/ab/absent.txt"}); err == nil {
		t.Fatal("missing media type must be unsupported")
	}
	mediaType := "text/plain"
	empty := createW13CFileRecord(t, root, "w13c-empty", &mediaType, []byte("   \n"))
	if _, err := BuildVectorStoreChunks(root, empty); err == nil || !strings.Contains(err.Error(), "可索引文本") {
		t.Fatalf("blank text = %v", err)
	}
	big := FileRecord{ID: "w13c-big", StorageKey: "files/ab/absent.txt", Bytes: int64(1) << 40, MediaType: &mediaType}
	if _, err := ReadFileTextForIndexing(root, big); err == nil || !strings.Contains(err.Error(), "大小限制") {
		t.Fatalf("oversized record = %v", err)
	}
}

func createW13CFileRecord(t *testing.T, root, id string, mediaType *string, content []byte) FileRecord {
	t.Helper()
	storageKey := StorageKeyForFile(id)
	path, err := EnsureFileObjectParent(root, storageKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return FileRecord{ID: id, StorageKey: storageKey, Bytes: int64(len(content)), MediaType: mediaType}
}

func TestW13CGeminiNativeSourceArms(t *testing.T) {
	// responseMimeType / responseSchema 的非对象与非法形态。
	if got := responseMimeTypeFromSourceForGeminiNative(map[string]any{}); got != "" {
		t.Fatalf("empty body mime = %q", got)
	}
	if got := responseMimeTypeFromSourceForGeminiNative(map[string]any{
		"response_format": map[string]any{"type": "json_object"},
	}); got != "application/json" {
		t.Fatalf("json_object mime = %q", got)
	}
	if got := responseMimeTypeFromSourceForGeminiNative(map[string]any{
		"text": map[string]any{"format": map[string]any{"type": "json_schema"}},
	}); got != "application/json" {
		t.Fatalf("text.format mime = %q", got)
	}
	if got := responseMimeTypeFromSourceForGeminiNative(map[string]any{
		"response_format": "not-an-object",
	}); got != "" {
		t.Fatalf("non-object format = %q", got)
	}
	if got := responseSchemaFromSourceForGeminiNative(map[string]any{}); got != nil {
		t.Fatalf("empty body schema = %v", got)
	}
	if got := responseSchemaFromSourceForGeminiNative(map[string]any{
		"response_format": map[string]any{"json_schema": map[string]any{"schema": "not-object"}},
	}); got != nil {
		t.Fatalf("non-object schema = %v", got)
	}
	schema := map[string]any{"type": "object"}
	if got := responseSchemaFromSourceForGeminiNative(map[string]any{
		"response_format": map[string]any{"json_schema": map[string]any{"schema": schema}},
	}); got == nil {
		t.Fatal("valid snake schema must resolve")
	}
	if got := responseSchemaFromSourceForGeminiNative(map[string]any{
		"text": map[string]any{"format": map[string]any{"json_schema": map[string]any{"schema": schema}}},
	}); got == nil {
		t.Fatal("valid text.format schema must resolve")
	}
	// source 内容到 Gemini parts 的非映射形态。
	parts, err := openAIContentToGeminiPartsForGeminiNative("plain text", BridgeRequestBodyOptions{})
	if err != nil || len(parts) == 0 {
		t.Fatalf("string content parts = %v err=%v", parts, err)
	}
	parts, err = openAIContentToGeminiPartsForGeminiNative([]any{nil, 5}, BridgeRequestBodyOptions{})
	if err != nil || len(parts) != 0 {
		t.Fatalf("invalid array content = %v err=%v", parts, err)
	}
	if _, err := openAIContentToGeminiPartsForGeminiNative(5, BridgeRequestBodyOptions{}); err == nil {
		t.Fatal("numeric content must fail")
	}
}
