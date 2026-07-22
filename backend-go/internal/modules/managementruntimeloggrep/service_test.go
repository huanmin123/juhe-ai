package managementruntimeloggrep

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNormalizeInputMatchesRuntimeLogGrepContract(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	input := normalizeInput(Input{
		Keywords: []string{"  Needle, second  ", "NEEDLE", "x", strings.Repeat("a", 140)},
		Limit:    900,
		StartAt:  now.Add(-10 * 24 * time.Hour),
		EndAt:    now.Add(time.Hour),
	}, nil, now)

	if len(input.Keywords) != 3 || input.Keywords[0] != "Needle" || input.Keywords[1] != "second" || len([]rune(input.Keywords[2])) != 128 {
		t.Fatalf("keywords = %#v", input.Keywords)
	}
	if input.ShortKeywordCount != 1 || input.Limit != 100 {
		t.Fatalf("short=%d limit=%d", input.ShortKeywordCount, input.Limit)
	}
	if !input.StartAt.Equal(now.Add(-7*24*time.Hour)) || !input.EndAt.Equal(now) || !input.Adjusted {
		t.Fatalf("range = %s - %s adjusted=%v", input.StartAt, input.EndAt, input.Adjusted)
	}
}

func TestServiceUsesArgumentArrayAndReturnsLatestSafeMatches(t *testing.T) {
	directory := t.TempDir()
	oldPath := writeLogFixture(t, directory, "juhe-ai.old.log", time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC))
	newPath := writeLogFixture(t, directory, "juhe-ai.log", time.Date(2026, 7, 22, 11, 0, 0, 0, time.UTC))
	var gotExecutable string
	var gotArgs []string
	runner := func(_ context.Context, executable string, args []string, onLine func([]byte) bool) (rgExitState, error) {
		gotExecutable = executable
		gotArgs = append([]string(nil), args...)
		lines := []string{
			rgMatchJSON(oldPath, 1, `{"time":"2026-07-22T09:00:00Z","level":30,"event":"gateway.old","msg":"Needle second"}`),
			rgMatchJSON(newPath, 1, `{"time":"2026-07-22T11:00:00Z","level":40,"traceId":"trace-1","event":"gateway.new","msg":"Needle second"}`),
			rgMatchJSON(newPath, 2, `{"time":"2026-07-22T11:01:00Z","level":30,"event":"http_request_completed","path":"/__aisys__/api/runtime-logs/grep","msg":"Needle second"}`),
			rgMatchJSON(newPath, 3, `{"time":"2026-07-22T11:02:00Z","level":30,"event":"large","msg":"Needle second","body":"`+strings.Repeat("x", 20_100)+`"}`),
		}
		for _, line := range lines {
			if !onLine([]byte(line)) {
				break
			}
		}
		return rgMatched, nil
	}
	service := newServiceWithDependencies(Options{
		Directory:     directory,
		FileEnabled:   true,
		MaxFiles:      2,
		RetentionDays: 30,
		RGPath:        "C:/tools/rg.exe",
	}, runner, func() time.Time { return time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC) })

	result := service.Grep(context.Background(), Input{Keywords: []string{"needle", "SECOND"}, Limit: 10})

	if !result.Available || len(result.Items) != 2 || result.Items[0].Event != "gateway.new" || result.Items[1].Event != "gateway.old" {
		t.Fatalf("result = %+v", result)
	}
	if result.Items[0].Level != "warn" || result.Items[0].TraceID != "trace-1" {
		t.Fatalf("item = %+v", result.Items[0])
	}
	if strings.Contains(result.Items[0].File, directory) || strings.Contains(result.Items[0].ID, directory) || result.Items[0].File != result.Items[0].FileName {
		t.Fatalf("filesystem path leaked in item: %+v", result.Items[0])
	}
	if gotExecutable != "C:/tools/rg.exe" {
		t.Fatalf("executable = %q", gotExecutable)
	}
	wantPrefix := []string{"--json", "--fixed-strings", "--ignore-case", "--no-heading", "--color=never", "--max-columns", "20000", "--", "needle"}
	if len(gotArgs) < len(wantPrefix)+2 {
		t.Fatalf("args = %#v", gotArgs)
	}
	for index, value := range wantPrefix {
		if gotArgs[index] != value {
			t.Fatalf("args[%d] = %q, want %q; all=%#v", index, gotArgs[index], value, gotArgs)
		}
	}
	if !containsString(gotArgs, oldPath) || !containsString(gotArgs, newPath) {
		t.Fatalf("file arguments = %#v", gotArgs)
	}
}

func TestServiceCapsParsedMatchesAndSingleConcurrency(t *testing.T) {
	directory := t.TempDir()
	path := writeLogFixture(t, directory, "juhe-ai.log", time.Now())
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	runner := func(_ context.Context, _ string, _ []string, onLine func([]byte) bool) (rgExitState, error) {
		once.Do(func() { close(entered) })
		<-release
		for index := 0; index < 2_050; index++ {
			line := `{"time":"2026-07-22T11:00:00Z","level":30,"event":"cap","msg":"parsecapneedle"}`
			if !onLine([]byte(rgMatchJSON(path, index+1, line))) {
				return rgMatched, nil
			}
		}
		return rgMatched, nil
	}
	service := newServiceWithDependencies(Options{Directory: directory, FileEnabled: true, MaxFiles: 1, RGPath: "rg"}, runner, time.Now)
	firstDone := make(chan Result, 1)
	go func() {
		firstDone <- service.Grep(context.Background(), Input{Keywords: []string{"parsecapneedle"}, Limit: 10})
	}()
	<-entered
	busy := service.Grep(context.Background(), Input{Keywords: []string{"parsecapneedle"}, Limit: 10})
	if busy.Available || !strings.Contains(busy.Message, "已有 grep 搜索正在运行") {
		t.Fatalf("busy result = %+v", busy)
	}
	close(release)
	result := <-firstDone
	if !result.Available || len(result.Items) != 10 || !result.Truncated || !strings.Contains(result.Message, "安全解析上限 2000") {
		t.Fatalf("capped result = %+v", result)
	}
}

func TestServiceCapsRGMatchEventsBeforeSecondaryKeywordFiltering(t *testing.T) {
	directory := t.TempDir()
	path := writeLogFixture(t, directory, "juhe-ai.log", time.Now())
	delivered := 0
	runner := func(_ context.Context, _ string, _ []string, onLine func([]byte) bool) (rgExitState, error) {
		for index := 0; index < 2_050; index++ {
			delivered++
			line := `{"time":"2026-07-22T11:00:00Z","level":30,"event":"cap","msg":"primaryneedle only"}`
			if !onLine([]byte(rgMatchJSON(path, index+1, line))) {
				break
			}
		}
		return rgMatched, nil
	}
	service := newServiceWithDependencies(Options{Directory: directory, FileEnabled: true, MaxFiles: 1, RGPath: "rg"}, runner, time.Now)

	result := service.Grep(context.Background(), Input{Keywords: []string{"primaryneedle", "secondaryneedle"}, Limit: 10})

	if delivered != 2_000 || !result.Available || !result.Truncated || len(result.Items) != 0 || !strings.Contains(result.Message, "安全解析上限 2000") {
		t.Fatalf("delivered=%d result=%+v", delivered, result)
	}
}

func TestServiceSharesOneSearchDeadlineAcrossRGBatches(t *testing.T) {
	directory := t.TempDir()
	modified := time.Now().Add(-time.Minute)
	for index := 0; index < 260; index++ {
		name := fmt.Sprintf("%s-%03d.log", strings.Repeat("long-name-", 10), index)
		writeLogFixture(t, directory, name, modified)
	}
	deadlines := []time.Time{}
	runner := func(ctx context.Context, _ string, _ []string, _ func([]byte) bool) (rgExitState, error) {
		if deadline, ok := ctx.Deadline(); ok {
			deadlines = append(deadlines, deadline)
		}
		return rgNoMatch, nil
	}
	service := newServiceWithDependencies(Options{Directory: directory, FileEnabled: true, MaxFiles: 500, RGPath: "rg"}, runner, time.Now)

	result := service.Grep(context.Background(), Input{Keywords: []string{"deadline-needle"}})

	if !result.Available || len(deadlines) < 2 {
		t.Fatalf("result=%+v deadlines=%v", result, deadlines)
	}
	for _, deadline := range deadlines[1:] {
		if !deadline.Equal(deadlines[0]) {
			t.Fatalf("batch deadlines differ: %v", deadlines)
		}
	}
}

func TestFilterFilesUsesFileLifetimeOverlap(t *testing.T) {
	files := []logFile{{
		name:     "juhe-ai.log",
		size:     1,
		started:  time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC),
		modified: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC),
	}}
	rangeInput := normalizedInput{
		StartAt: time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC),
		EndAt:   time.Date(2026, 7, 22, 11, 0, 0, 0, time.UTC),
	}
	if got := filterFiles(files, rangeInput); len(got) != 1 {
		t.Fatalf("overlapping active file excluded: %+v", got)
	}
}

func TestFilterFilesUsesUnknownStartConservatively(t *testing.T) {
	files := []logFile{{
		name:     "juhe-ai.log",
		size:     1,
		started:  time.Time{},
		modified: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC),
	}}
	rangeInput := normalizedInput{
		StartAt: time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC),
		EndAt:   time.Date(2026, 7, 22, 11, 0, 0, 0, time.UTC),
	}
	if got := filterFiles(files, rangeInput); len(got) != 1 {
		t.Fatalf("unknown file start must not cause a false exclusion: %+v", got)
	}
}

func TestServiceSafelyDowngradesMissingAndFailedRG(t *testing.T) {
	directory := t.TempDir()
	writeLogFixture(t, directory, "juhe-ai.log", time.Now())
	missing := newServiceWithDependencies(Options{
		Directory:   directory,
		FileEnabled: true,
		MaxFiles:    1,
		LookPath:    func(string) (string, error) { return "", exec.ErrNotFound },
	}, nil, time.Now).Grep(context.Background(), Input{Keywords: []string{"needle"}})
	if missing.Available || !strings.Contains(missing.Message, "未找到 rg") || strings.Contains(missing.Message, directory) {
		t.Fatalf("missing result = %+v", missing)
	}

	failed := newServiceWithDependencies(Options{Directory: directory, FileEnabled: true, MaxFiles: 1, RGPath: filepath.Join(directory, "secret-rg")},
		func(context.Context, string, []string, func([]byte) bool) (rgExitState, error) {
			return rgFailed, errors.New("secret command failure at " + directory)
		}, time.Now).Grep(context.Background(), Input{Keywords: []string{"needle"}})
	if failed.Available || !strings.Contains(failed.Message, "rg 执行失败") || strings.Contains(failed.Message, "secret") || strings.Contains(failed.Message, directory) {
		t.Fatalf("failed result leaked internals: %+v", failed)
	}
}

func TestServiceRealRGFixture(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg is not installed")
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "juhe-ai.log")
	content := strings.Join([]string{
		`{"time":"2026-07-22T11:00:00Z","level":30,"event":"real","msg":"realneedle"}`,
		`{"time":"2026-07-22T11:01:00Z","level":30,"event":"other","msg":"other"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewService(Options{Directory: directory, FileEnabled: true, MaxFiles: 1})
	result := service.Grep(context.Background(), Input{Keywords: []string{"realneedle"}, Limit: 10})
	if !result.Available || len(result.Items) != 1 || result.Items[0].Event != "real" {
		t.Fatalf("real rg result = %+v", result)
	}
}

func writeLogFixture(t *testing.T, directory, name string, modified time.Time) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
	return path
}

func rgMatchJSON(path string, lineNumber int, line string) string {
	var record any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		panic(err)
	}
	lineBytes, _ := json.Marshal(record)
	event := map[string]any{
		"type": "match",
		"data": map[string]any{
			"path":        map[string]any{"text": path},
			"lines":       map[string]any{"text": string(lineBytes) + "\n"},
			"line_number": lineNumber,
		},
	}
	value, _ := json.Marshal(event)
	return string(value)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
