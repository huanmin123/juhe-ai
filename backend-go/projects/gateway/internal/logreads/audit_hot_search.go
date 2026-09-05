package logreads

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// ---------------------------------------------------------------------------
// Audit hot search (audit-log-f3-query.repository.ts searchHot + the file
// helpers below it). The NDJSON buckets are written by the F3 writer
// (internal/auditlog hot_search.go): {"auditLogId","createdAt","traceId"?,
// "trafficSource"?,"sequence","text"} one object per line inside
// audit-hot-YYYYMMDDHH.ndjson hourly buckets.
//
// Node contract quirks kept byte-faithful: the scan window always collapses
// to [endAt-1h, min(endAt, now)] (the startAt clamp makes startAt ==
// endAt-1h in every branch), only the two newest buckets in range are read,
// and the 4 MiB / 10k line budgets drive the truncation messages.
// ---------------------------------------------------------------------------

const (
	auditHotMaxFiles            = 2
	auditHotMaxDirectoryEntries = 4096
	auditHotMaxScanBytes        = 4 * 1024 * 1024
	auditHotMaxScanLines        = 10_000
	auditHotMaxLineBytes        = 256 * 1024
	auditHotReadChunkBytes      = 64 * 1024
	auditHotMaxKeywords         = 10
	auditHotMinKeywordRunes     = 2
	auditHotDefaultLimit        = 100
	auditHotWindow              = time.Hour
)

// isAuditHotBucketName mirrors /^audit-hot-\d{10}\.ndjson$/ and returns the
// bucket hour.
func isAuditHotBucketName(name string) (time.Time, bool) {
	if !strings.HasPrefix(name, "audit-hot-") || !strings.HasSuffix(name, ".ndjson") {
		return time.Time{}, false
	}
	value := strings.TrimSuffix(strings.TrimPrefix(name, "audit-hot-"), ".ndjson")
	if len(value) != 10 {
		return time.Time{}, false
	}
	bucket, err := time.Parse("2006010215", value)
	if err != nil {
		return time.Time{}, false
	}
	return bucket.UTC(), true
}

// AuditHotSearchOptions mirrors AuditLogF3HotSearchOptions.
type AuditHotSearchOptions struct {
	Keywords []string
	Limit    *int
	StartAt  string
	EndAt    string
}

// AuditHotSearchScan is the mechanical searchHot result; the route handler
// merges it with ListAuditLogsByID into the response envelope.
type AuditHotSearchScan struct {
	Available        bool
	Keywords         []string
	StartAt          string
	EndAt            string
	Limit            int
	AuditLogIDs      []string
	Truncated        bool
	ScannedFileCount int
	Message          string
}

// auditHotSearchLine is the persisted bucket line (auditlog.hotSearchLine).
type auditHotSearchLine struct {
	AuditLogID string `json:"auditLogId"`
	CreatedAt  string `json:"createdAt"`
	Text       string `json:"text"`
}

// normalizeHotSearchKeywords mirrors normalizeHotKeywords: trim, lowercase,
// at least two runes, dedupe, at most ten keywords.
func normalizeHotSearchKeywords(values []string) []string {
	seen := make(map[string]bool, len(values))
	keywords := make([]string, 0, len(values))
	for _, value := range values {
		keyword := strings.ToLower(strings.TrimSpace(value))
		if len([]rune(keyword)) < auditHotMinKeywordRunes || seen[keyword] {
			continue
		}
		seen[keyword] = true
		keywords = append(keywords, keyword)
		if len(keywords) >= auditHotMaxKeywords {
			break
		}
	}
	return keywords
}

// normalizeHotSearchLimit mirrors normalizeHotLimit: default 100, clamp 1..100.
func normalizeHotSearchLimit(value *int) int {
	if value == nil {
		return auditHotDefaultLimit
	}
	return min(100, max(1, *value))
}

// parseHotInstantMillis mirrors f3TimestampMilliseconds: strict RFC3339 with
// millisecond precision (JavaScript Date granularity).
func parseHotInstantMillis(value string) (int64, error) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return 0, readParamErrorf("F3 热搜索时间必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	return parsed.UnixMilli(), nil
}

// SearchHot runs the Node-contract hot search over the configured bucket
// directory. Missing keywords and the unconfigured directory are response
// contracts (available flag + message), not errors.
func (s *auditLogSQLReader) SearchHot(ctx context.Context, options AuditHotSearchOptions) (AuditHotSearchScan, error) {
	keywords := normalizeHotSearchKeywords(options.Keywords)
	limit := normalizeHotSearchLimit(options.Limit)
	now := s.now().UnixMilli()
	endMillis := now
	if strings.TrimSpace(options.EndAt) != "" {
		parsed, err := parseHotInstantMillis(options.EndAt)
		if err != nil {
			return AuditHotSearchScan{}, err
		}
		endMillis = min(parsed, now)
	}
	// Node: startMs = max(endMs-1h, min(startAt, endMs-1h)) == endMs-1h.
	startMillis := endMillis - auditHotWindow.Milliseconds()
	scan := AuditHotSearchScan{
		Available:   true,
		Keywords:    keywords,
		StartAt:     millisToISO(startMillis),
		EndAt:       millisToISO(endMillis),
		Limit:       limit,
		AuditLogIDs: []string{},
	}
	if len(keywords) == 0 {
		scan.Message = "请输入要搜索的审计内容关键字"
		return scan, nil
	}
	if s.hotDir == "" {
		scan.Available = false
		scan.Message = "F3 审计内容搜索目录未配置"
		return scan, nil
	}
	paths, rangeTruncated, err := listAuditHotBuckets(s.hotDir, startMillis, endMillis)
	if err != nil {
		return AuditHotSearchScan{}, err
	}
	if paths == nil {
		scan.Message = "最近 1 小时没有可搜索的审计内容"
		return scan, nil
	}
	seen := make(map[string]int64, 16)
	remainingBytes := int64(auditHotMaxScanBytes)
	remainingLines := auditHotMaxScanLines
	contentTruncated := false
	for _, path := range paths {
		if remainingBytes <= 0 || remainingLines <= 0 {
			contentTruncated = true
			break
		}
		fileTruncated, scanErr := scanAuditHotBucket(ctx, path, keywords, startMillis, endMillis, &remainingBytes, &remainingLines, seen)
		if scanErr != nil {
			return AuditHotSearchScan{}, scanErr
		}
		scan.ScannedFileCount++
		if fileTruncated {
			contentTruncated = true
		}
	}
	type match struct {
		id   string
		when int64
	}
	matches := make([]match, 0, len(seen))
	for id, when := range seen {
		matches = append(matches, match{id: id, when: when})
	}
	sort.Slice(matches, func(left, right int) bool {
		if matches[left].when != matches[right].when {
			return matches[left].when > matches[right].when
		}
		return matches[left].id < matches[right].id
	})
	resultTruncated := len(matches) > limit
	scan.Truncated = rangeTruncated || contentTruncated || resultTruncated
	if resultTruncated {
		matches = matches[:limit]
	}
	for _, item := range matches {
		scan.AuditLogIDs = append(scan.AuditLogIDs, item.id)
	}
	messages := make([]string, 0, 3)
	if rangeTruncated {
		messages = append(messages, "热搜索文件范围超过读取上限，结果可能不完整")
	}
	if contentTruncated {
		messages = append(messages, "热搜索内容超过读取上限，结果可能不完整")
	}
	if resultTruncated {
		messages = append(messages, "结果超过 "+strconv.Itoa(limit)+" 条，已按最新优先截断显示")
	}
	scan.Message = strings.Join(messages, "；")
	return scan, nil
}

func millisToISO(millis int64) string {
	return time.UnixMilli(millis).UTC().Format("2006-01-02T15:04:05.000Z")
}

// listAuditHotBuckets mirrors listF3HotSearchFiles: nil paths (without
// error) means the directory itself is missing.
func listAuditHotBuckets(directory string, startMillis, endMillis int64) ([]string, bool, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	type candidate struct {
		path   string
		bucket int64
	}
	var candidates []candidate
	entryTruncated := false
	for index, entry := range entries {
		if index >= auditHotMaxDirectoryEntries {
			entryTruncated = true
			break
		}
		if !entry.Type().IsRegular() {
			continue
		}
		bucket, ok := isAuditHotBucketName(entry.Name())
		if !ok {
			continue
		}
		bucketMillis := bucket.UnixMilli()
		if bucketMillis+auditHotWindow.Milliseconds() < startMillis || bucketMillis > endMillis {
			continue
		}
		candidates = append(candidates, candidate{path: filepath.Join(directory, entry.Name()), bucket: bucketMillis})
	}
	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].bucket != candidates[right].bucket {
			return candidates[left].bucket > candidates[right].bucket
		}
		return candidates[left].path > candidates[right].path
	})
	truncated := entryTruncated || len(candidates) > auditHotMaxFiles
	if len(candidates) > auditHotMaxFiles {
		candidates = candidates[:auditHotMaxFiles]
	}
	paths := make([]string, 0, len(candidates))
	for _, item := range candidates {
		paths = append(paths, item.path)
	}
	return paths, truncated, nil
}

// scanAuditHotBucket mirrors scanF3HotSearchFile: it consumes the shared
// byte/line budgets (mutated in place across files) and reports whether the
// file was cut short (budget exhausted or an oversized line was dropped).
func scanAuditHotBucket(ctx context.Context, path string, keywords []string, startMillis, endMillis int64, remainingBytes *int64, remainingLines *int, seen map[string]int64) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, auditHotReadChunkBytes)
	truncated := false
	for {
		select {
		case <-ctx.Done():
			return truncated, ctx.Err()
		default:
		}
		if *remainingBytes <= 0 || *remainingLines <= 0 {
			return true, nil
		}
		raw, consumed, cleanEOF, tooLong, readErr := readBoundedHotLine(reader, auditHotMaxLineBytes)
		if readErr != nil {
			return truncated, readErr
		}
		*remainingBytes -= consumed
		if tooLong {
			truncated = true
			*remainingLines--
			if cleanEOF {
				return truncated, nil
			}
			continue
		}
		if raw == nil {
			return truncated, nil
		}
		*remainingLines--
		collectAuditHotMatch(string(raw), keywords, startMillis, endMillis, seen)
		if cleanEOF {
			return truncated, nil
		}
	}
}

// readBoundedHotLine reads one \n-terminated line. raw is nil at clean EOF;
// tooLong marks a line over maxBytes (the rest of it is drained, matching
// the Node discard behaviour). consumed counts the raw bytes including the
// newline so the shared byte budget behaves like the Node chunk reader.
func readBoundedHotLine(reader *bufio.Reader, maxBytes int) (raw []byte, consumed int64, cleanEOF bool, tooLong bool, err error) {
	var out []byte
	for {
		chunk, readErr := reader.ReadSlice('\n')
		consumed += int64(len(chunk))
		switch {
		case readErr == nil:
			chunk = chunk[:len(chunk)-1] // drop the newline
			out = append(out, chunk...)
			if len(out) > maxBytes {
				return nil, consumed, false, true, nil
			}
			return out, consumed, false, false, nil
		case errors.Is(readErr, bufio.ErrBufferFull):
			out = append(out, chunk...)
			if len(out) > maxBytes {
				if drainErr := drainRestOfHotLine(reader); drainErr != nil {
					return nil, consumed, false, false, drainErr
				}
				return nil, consumed, false, true, nil
			}
		case errors.Is(readErr, io.EOF):
			out = append(out, chunk...)
			if len(out) == 0 {
				return nil, consumed, true, false, nil
			}
			if len(out) > maxBytes {
				return nil, consumed, true, true, nil
			}
			return out, consumed, true, false, nil
		default:
			return nil, consumed, false, false, readErr
		}
	}
}

// drainRestOfLine discards the remainder of an oversized line.
func drainRestOfHotLine(reader *bufio.Reader) error {
	for {
		_, err := reader.ReadSlice('\n')
		if err == nil {
			return nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
}

// collectAuditHotMatch mirrors collectF3HotSearchMatch: JSON lines with an
// auditLogId inside the window whose lowercased text contains ANY keyword.
func collectAuditHotMatch(line string, keywords []string, startMillis, endMillis int64, seen map[string]int64) {
	line = strings.TrimSuffix(line, "\r")
	var parsed auditHotSearchLine
	if err := json.Unmarshal([]byte(line), &parsed); err != nil {
		return
	}
	if strings.TrimSpace(parsed.AuditLogID) == "" {
		return
	}
	when, err := time.Parse(time.RFC3339Nano, parsed.CreatedAt)
	if err != nil {
		return
	}
	millis := when.UnixMilli()
	if millis < startMillis || millis > endMillis {
		return
	}
	text := strings.ToLower(parsed.Text)
	for _, keyword := range keywords {
		if strings.Contains(text, keyword) {
			if previous, ok := seen[parsed.AuditLogID]; !ok || millis > previous {
				seen[parsed.AuditLogID] = millis
			}
			return
		}
	}
}

// handleAuditSearchHot mirrors GET /search-hot: the scan + the by-id item
// hydration merged into the Node envelope.
func (d *ReadsDeps) handleAuditSearchHot(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	options, err := parseAuditHotSearchOptions(r)
	if readWriteStoreError(w, err) {
		return
	}
	scan, err := d.Audit.SearchHot(r.Context(), options)
	if readWriteStoreError(w, err) {
		return
	}
	items, err := d.Audit.ListAuditLogsByID(r.Context(), scan.AuditLogIDs)
	if readWriteStoreError(w, err) {
		return
	}
	total := int64(len(items))
	if scan.Truncated {
		total = max(int64(len(items)+1), int64(len(scan.AuditLogIDs)))
	}
	kernel.WriteOK(w, struct {
		Items            []AuditLogListItem `json:"items"`
		Total            int64              `json:"total"`
		HasMore          bool               `json:"hasMore"`
		Page             int                `json:"page"`
		PageSize         int                `json:"pageSize"`
		Available        bool               `json:"available"`
		ElapsedMs        int64              `json:"elapsedMs"`
		Keywords         []string           `json:"keywords"`
		StartAt          string             `json:"startAt"`
		EndAt            string             `json:"endAt"`
		Limit            int                `json:"limit"`
		Truncated        bool               `json:"truncated"`
		ScannedFileCount int                `json:"scannedFileCount"`
		Message          string             `json:"message,omitempty"`
	}{
		Items:            items,
		Total:            total,
		HasMore:          scan.Truncated,
		Page:             1,
		PageSize:         scan.Limit,
		Available:        scan.Available,
		ElapsedMs:        time.Since(started).Milliseconds(),
		Keywords:         scan.Keywords,
		StartAt:          scan.StartAt,
		EndAt:            scan.EndAt,
		Limit:            scan.Limit,
		Truncated:        scan.Truncated,
		ScannedFileCount: scan.ScannedFileCount,
		Message:          scan.Message,
	}, "")
}

// parseAuditHotSearchOptions mirrors parseAuditHotSearchOptions: repeated
// keywords values (Node stringArrayQueryValues), finite limit and the strict
// date range (400 on invalid bounds).
func parseAuditHotSearchOptions(r *http.Request) (AuditHotSearchOptions, error) {
	startAt, endAt, err := readQueryDateTimeRange(r)
	if err != nil {
		return AuditHotSearchOptions{}, err
	}
	keywords := append([]string{}, r.URL.Query()["keywords"]...)
	return AuditHotSearchOptions{
		Keywords: keywords,
		Limit:    readQueryInt(r, "limit"),
		StartAt:  startAt,
		EndAt:    endAt,
	}, nil
}
