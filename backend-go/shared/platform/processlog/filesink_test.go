package processlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// 轮转文件名的写侧正则镜像；外部采集按该命名识别历史文件。
var filesinkRotationNamePattern = regexp.MustCompile(`^(.*)\.(\d{8}T\d{6}Z)\.([0-9a-f-]+)\.log$`)

func TestFileSinkRotatesBySizeAndKeepsJSONLLinesIntact(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 9, 28, 8, 30, 0, 0, time.UTC)
	// 时间与 token 逐次递增：同秒多次轮转的文件名也不得冲突（消费面按
	// 文件名区分轮转文件）。
	nowCalls, tokenCalls := 0, 0
	sink, err := NewFileSink(dir, FileSinkOptions{
		MaxFileBytes: 400,
		Now: func() time.Time {
			nowCalls++
			return base.Add(time.Duration(nowCalls) * time.Second)
		},
		NewToken: func() string {
			tokenCalls++
			return fmt.Sprintf("0f1e2d3c-0000-0000-0000-%012x", tokenCalls)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sink.Close() }()

	logger := slog.New(slog.NewJSONHandler(sink, nil))
	total := 12
	for index := 0; index < total; index++ {
		logger.Info(fmt.Sprintf("中文记录-%02d", index), "event", "filesink_probe", "index", index)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	rotatedCount := 0
	currentSeen := false
	linesSeen := 0
	for _, entry := range entries {
		raw, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		linesSeen += countCompleteJSONLLines(t, raw)
		if entry.Name() == CurrentLogFileName {
			currentSeen = true
			continue
		}
		if !filesinkRotationNamePattern.MatchString(entry.Name()) {
			t.Fatalf("轮转文件名未命中消费面正则: %s", entry.Name())
		}
		rotatedCount++
	}
	if !currentSeen {
		t.Fatal("current 文件必须重建")
	}
	if rotatedCount == 0 {
		t.Fatal("超阈值写入必须产生至少一个轮转文件")
	}
	if linesSeen != total {
		t.Fatalf("行数守恒失败: 目录总行数=%d want %d", linesSeen, total)
	}
}

func TestFileSinkCustomInstanceFileNameAndRotation(t *testing.T) {
	dir := t.TempDir()
	sink, err := NewFileSink(dir, FileSinkOptions{
		FileName:     "juhe-ai.instance-9.log",
		MaxFileBytes: 180,
		Now:          func() time.Time { return time.Date(2026, 9, 28, 8, 30, 0, 0, time.UTC) },
		NewToken:     func() string { return "0f1e2d3c-0000-0000-0000-000000000001" },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sink.Close() }()
	if want := filepath.Join(dir, "juhe-ai.instance-9.log"); sink.CurrentPath() != want {
		t.Fatalf("current path = %q, want %q", sink.CurrentPath(), want)
	}
	logger := slog.New(slog.NewJSONHandler(sink, nil))
	for index := 0; index < 5; index++ {
		logger.Info("instance file rotation", "event", "filesink_instance_probe", "index", index)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	currentSeen := false
	rotatedSeen := false
	for _, entry := range entries {
		if entry.Name() == "juhe-ai.instance-9.log" {
			currentSeen = true
			continue
		}
		if strings.HasPrefix(entry.Name(), "juhe-ai.instance-9.") && filesinkRotationNamePattern.MatchString(entry.Name()) {
			rotatedSeen = true
		}
	}
	if !currentSeen || !rotatedSeen {
		t.Fatalf("instance file/rotation missing: current=%v rotated=%v entries=%v", currentSeen, rotatedSeen, entries)
	}
}

func TestFileSinkRejectsPathInFileName(t *testing.T) {
	for _, name := range []string{"../juhe-ai.log", "sub/juhe-ai.log", "juhe-ai.txt"} {
		if _, err := NewFileSink(t.TempDir(), FileSinkOptions{FileName: name}); err == nil {
			t.Fatalf("FileName %q must be rejected", name)
		}
	}
}

func TestFileSinkResumesFromExistingSizeOnReopen(t *testing.T) {
	dir := t.TempDir()
	sink, err := NewFileSink(dir, FileSinkOptions{MaxFileBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Write([]byte(`{"time":"2026-09-28T08:30:00Z","level":"INFO","msg":"first"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	sizeAfterFirstWrite := sink.CurrentSize()
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFileSink(dir, FileSinkOptions{MaxFileBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if reopened.CurrentSize() != sizeAfterFirstWrite {
		t.Fatalf("重开后必须回读真实文件大小: got %d want %d", reopened.CurrentSize(), sizeAfterFirstWrite)
	}
	if reopened.CurrentPath() != filepath.Join(dir, CurrentLogFileName) {
		t.Fatalf("unexpected current path: %s", reopened.CurrentPath())
	}
}

func TestFileSinkCloseIsIdempotentAndRejectsLaterWrites(t *testing.T) {
	sink, err := NewFileSink(t.TempDir(), FileSinkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("close 必须幂等: %v", err)
	}
	if _, err := sink.Write([]byte("after-close\n")); err == nil {
		t.Fatal("关闭后的 Write 必须返回错误")
	}
}

func TestFileSinkConcurrentWritesKeepWholeLines(t *testing.T) {
	dir := t.TempDir()
	sink, err := NewFileSink(dir, FileSinkOptions{MaxFileBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sink.Close() }()
	var group sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			logger := slog.New(slog.NewJSONHandler(sink, nil))
			for index := 0; index < 25; index++ {
				logger.Info(fmt.Sprintf("并发-%d-%02d", worker, index), "event", "filesink_concurrent")
			}
		}(worker)
	}
	group.Wait()
	raw, err := os.ReadFile(filepath.Join(dir, CurrentLogFileName))
	if err != nil {
		t.Fatal(err)
	}
	if lines := countCompleteJSONLLines(t, raw); lines != 8*25 {
		t.Fatalf("并发写入必须逐行完整: got %d lines want %d", lines, 8*25)
	}
}

func TestTeeWriterWritesEveryDestinationAndAggregatesFirstError(t *testing.T) {
	good := &bytes.Buffer{}
	tee := NewTeeWriter(failingWriter{err: fmt.Errorf("坏管道")}, good, failingWriter{err: fmt.Errorf("第二路失败")})
	if _, err := tee.Write([]byte("payload")); err == nil || err.Error() != "坏管道" {
		t.Fatalf("tee 必须聚合第一个错误: %v", err)
	}
	if good.String() != "payload" {
		t.Fatalf("失败路不得短路后续路: got %q", good.String())
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestNewRandomTokenMatchesConsumedShape(t *testing.T) {
	token := newRandomToken()
	if len(token) != 36 || strings.Count(token, "-") != 4 {
		t.Fatalf("token 必须是 8-4-4-4-12 形状: %q", token)
	}
	if !filesinkRotationNamePattern.MatchString("juhe-ai.20260928T083000Z." + token + ".log") {
		t.Fatalf("token 未命中消费面正则: %q", token)
	}
}

// countCompleteJSONLLines 断言每行都以 \n 结尾且是合法 JSON 对象，返回行数。
func countCompleteJSONLLines(t *testing.T, raw []byte) int {
	t.Helper()
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		t.Fatalf("JSONL 内容必须以换行结尾（got %d bytes）", len(raw))
	}
	lines := 0
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			t.Fatal("JSONL 不允许出现空行（撕裂行前兆）")
		}
		var value map[string]any
		if err := json.Unmarshal(line, &value); err != nil {
			t.Fatalf("行不是完整 JSON 对象（撕裂行）: %v: %s", err, line)
		}
		lines++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("扫描 JSONL 失败: %v", err)
	}
	return lines
}

// TestFileSinkReopensAfterWriteFailure 覆盖 NFS/共享目录断连语义：句柄失效
// 后 Write 返回错误并置空句柄，节流期内不反复 open；到点后自动重开并续写，
// 无需重启进程。
func TestFileSinkReopensAfterWriteFailure(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	sink, err := NewFileSink(dir, FileSinkOptions{
		RetryInterval: 5 * time.Second,
		Now:           func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	if _, err := sink.Write([]byte("{\"n\":1}\n")); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	// 模拟句柄失效（ESTALE/底层文件被关闭）。
	if err := sink.file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Write([]byte("{\"n\":2}\n")); err == nil {
		t.Fatal("失效句柄写入应返回错误")
	}
	// 节流期内不得重开：错误信息应提示等待重开。
	if _, err := sink.Write([]byte("{\"n\":3}\n")); err == nil || !strings.Contains(err.Error(), "暂不可用") {
		t.Fatalf("节流期内应返回不可用错误，got %v", err)
	}
	// 到点后自动重开并续写（模拟 NFS 恢复）。
	now = now.Add(6 * time.Second)
	if _, err := sink.Write([]byte("{\"n\":4}\n")); err != nil {
		t.Fatalf("恢复后写入失败: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, CurrentLogFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "\"n\":1") || !strings.Contains(string(raw), "\"n\":4") {
		t.Fatalf("恢复后内容不完整: %s", raw)
	}
}

// TestFileSinkReopenRetryIsThrottled 用注入的 openFile 断言重试只按节流
// 间隔发生（不会每次写都 open），恢复后立即续写。
func TestFileSinkReopenRetryIsThrottled(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 7, 11, 0, 0, 0, time.UTC)
	realOpen := os.OpenFile
	opens, failing := 0, true
	sink, err := NewFileSink(dir, FileSinkOptions{
		RetryInterval: 5 * time.Second,
		Now:           func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	sink.openFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		opens++
		if failing {
			return nil, os.ErrPermission
		}
		return realOpen(name, flag, perm)
	}
	// 让句柄失效，触发重开路径。
	_ = sink.file.Close()
	sink.file = nil
	sink.scheduleReopenLocked()
	if _, err := sink.Write([]byte("x\n")); err == nil {
		t.Fatal("节流期内写入应失败")
	}
	if opens != 0 {
		t.Fatalf("节流期内不应 open，got %d", opens)
	}
	now = now.Add(6 * time.Second)
	if _, err := sink.Write([]byte("y\n")); err == nil {
		t.Fatal("open 仍失败时应返回错误")
	}
	if opens != 1 {
		t.Fatalf("到点后应恰好 open 一次，got %d", opens)
	}
	failing = false
	now = now.Add(6 * time.Second)
	if _, err := sink.Write([]byte("z\n")); err != nil {
		t.Fatalf("恢复后写入失败: %v", err)
	}
	if opens != 2 {
		t.Fatalf("恢复时应再 open 一次，got %d", opens)
	}
}
