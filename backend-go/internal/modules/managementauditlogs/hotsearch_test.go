package managementauditlogs

import (
	"context"
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

func writeHotSearchFile(t *testing.T, root, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
