package managementauditlogs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHotSearchScannerMatchesAnyKeywordWithinBoundedWindow(t *testing.T) {
	root := t.TempDir()
	writeHotSearchFile(t, root, "audit-hot-2026072209.ndjson", strings.Join([]string{
		`{"auditLogId":"old","createdAt":"2026-07-22T08:59:59Z","text":"needle"}`,
		`{"auditLogId":"new","createdAt":"2026-07-22T09:30:00Z","text":"other"}`,
		`{"auditLogId":"newer","createdAt":"2026-07-22T09:45:00Z","text":"NEEDLE"}`,
	}, "\n"))

	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	result := newHotSearchScanner(root).Search(context.Background(), HotSearchInput{
		Keywords: []string{" needle ", "needle", "other", "x"}, Limit: 10,
		StartAt: now.Add(-time.Hour).Format(time.RFC3339), EndAt: now.Format(time.RFC3339), Now: now,
	})
	if !result.Available || result.Message != "" {
		t.Fatalf("result availability = %+v", result)
	}
	if len(result.IDs) != 2 || result.IDs[0].ID != "newer" || result.IDs[1].ID != "new" {
		t.Fatalf("ids = %+v", result.IDs)
	}
	if result.ScannedFileCount != 1 || result.Truncated {
		t.Fatalf("scan metadata = %+v", result)
	}
}

func TestHotSearchScannerMissingDirectoryIsAvailableWithoutResults(t *testing.T) {
	result := newHotSearchScanner(filepath.Join(t.TempDir(), "missing")).Search(context.Background(), HotSearchInput{
		Keywords: []string{"ab"}, Limit: 100, Now: time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC),
	})
	if !result.Available || len(result.IDs) != 0 || result.Message == "" {
		t.Fatalf("result = %+v", result)
	}
}

func TestHotSearchScannerReportsCanceledDirectoryScanAsTruncated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := newHotSearchScanner(t.TempDir()).Search(ctx, HotSearchInput{
		Keywords: []string{"needle"}, Now: time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC),
	})
	if !result.Available || !result.Truncated || !strings.Contains(result.Message, "安全边界") {
		t.Fatalf("result = %+v", result)
	}
}

func TestHotSearchScannerRejectsConcurrentSearchWithoutScanning(t *testing.T) {
	scanner := newHotSearchScanner(t.TempDir())
	scanner.slot <- struct{}{}
	result := scanner.Search(context.Background(), HotSearchInput{
		Keywords: []string{"needle"}, Now: time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC),
	})
	<-scanner.slot
	if result.Available || !strings.Contains(result.Message, "正在运行") {
		t.Fatalf("result = %+v", result)
	}
}

func TestHotSearchScannerRejectsUnreadableDependencyWithoutLeakingPath(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := newHotSearchScanner(file).Search(context.Background(), HotSearchInput{
		Keywords: []string{"ab"}, Limit: 100, Now: time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC),
	})
	if result.Available || !strings.Contains(result.Message, "不可用") || strings.Contains(result.Message, file) {
		t.Fatalf("result = %+v", result)
	}
}

func TestHotSearchScannerCapsResultsAndSkipsMalformedOrOversizedLines(t *testing.T) {
	root := t.TempDir()
	longText := strings.Repeat("x", maxHotSearchLineBytes+1)
	writeHotSearchFile(t, root, "audit-hot-2026072210.ndjson", strings.Join([]string{
		`not json`,
		`{"auditLogId":"ignored","createdAt":"2026-07-22T10:00:00Z","text":"` + longText + `"}`,
		`{"auditLogId":"a","createdAt":"2026-07-22T10:00:00Z","text":"needle"}`,
		`{"auditLogId":"b","createdAt":"2026-07-22T10:00:00Z","text":"needle"}`,
	}, "\n"))
	result := newHotSearchScanner(root).Search(context.Background(), HotSearchInput{
		Keywords: []string{"needle"}, Limit: 1, Now: time.Date(2026, 7, 22, 11, 0, 0, 0, time.UTC),
	})
	if !result.Available || len(result.IDs) != 1 || result.IDs[0].ID != "a" || !result.Truncated {
		t.Fatalf("result = %+v", result)
	}
}

func TestHotSearchFileListingOnlyTruncatesBeyondDirectoryEntryLimit(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < maxHotSearchFiles; index++ {
		writeHotSearchFile(t, root, fmt.Sprintf("ignored-%04d", index), "")
	}
	scanner := newHotSearchScanner(root)
	start := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)

	listing, err := scanner.listFiles(context.Background(), start, start.Add(time.Hour))
	if err != nil || listing.truncated {
		t.Fatalf("exact-boundary listing = %+v, err = %v", listing, err)
	}
	writeHotSearchFile(t, root, "ignored-overflow", "")
	listing, err = scanner.listFiles(context.Background(), start, start.Add(time.Hour))
	if err != nil || !listing.truncated {
		t.Fatalf("overflow listing = %+v, err = %v", listing, err)
	}
}

func TestNormalizeHotSearchLimitDistinguishesMissingAndExplicitNonPositiveValues(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		input HotSearchInput
		want  int
	}{
		{name: "missing", input: HotSearchInput{}, want: 100},
		{name: "explicit zero", input: HotSearchInput{LimitProvided: true}, want: 1},
		{name: "negative", input: HotSearchInput{Limit: -10, LimitProvided: true}, want: 1},
		{name: "upper bound", input: HotSearchInput{Limit: 101, LimitProvided: true}, want: 100},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := normalizeHotSearchLimit(testCase.input); got != testCase.want {
				t.Fatalf("normalizeHotSearchLimit() = %d, want %d", got, testCase.want)
			}
		})
	}
}

func TestParseHotSearchTimeMatchesNodeDateParseFormats(t *testing.T) {
	fallback := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	localTime := time.Date(2026, 7, 22, 9, 30, 0, 0, time.Local).UTC()
	for _, testCase := range []struct {
		name  string
		value string
		want  time.Time
	}{
		{name: "date only", value: "2026-07-22", want: time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)},
		{name: "year month", value: "2026-07", want: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)},
		{name: "local minute", value: "2026-07-22T09:30", want: localTime},
		{name: "offset without colon", value: "2026-07-22T09:30:00+0800", want: time.Date(2026, 7, 22, 1, 30, 0, 0, time.UTC)},
		{name: "invalid", value: "not-a-date", want: fallback},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := parseHotSearchTime(testCase.value, fallback); !got.Equal(testCase.want) {
				t.Fatalf("parseHotSearchTime(%q) = %s, want %s", testCase.value, got, testCase.want)
			}
		})
	}
}

func writeHotSearchFile(t *testing.T, root, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
