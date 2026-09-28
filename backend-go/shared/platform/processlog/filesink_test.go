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

// 消费面轮转名正则的写侧镜像（jobs runtimelog filename.go 同款）；写侧
// 产出的轮转文件名必须整串命中，否则索引器会跳过该文件。
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
