package managementauditlogs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultHotSearchLimit    = 100
	maxHotSearchLimit        = 100
	maxHotSearchKeywords     = 10
	minHotSearchKeywordRunes = 2
	maxHotSearchKeywordRunes = 128
	maxHotSearchLineRunes    = 20_000
	maxHotSearchLineBytes    = maxHotSearchLineRunes * utf8.UTFMax
	maxHotSearchIDBytes      = 256
	maxHotSearchFiles        = 2_000
	maxHotSearchMatches      = 2_000
	hotSearchReadBlockBytes  = 64 * 1024
	maxHotSearchDuration     = 15 * time.Second
	hotSearchWindow          = time.Hour
)

type HotSearchInput struct {
	Keywords       []string
	Limit          int
	LimitProvided  bool
	StartAt, EndAt string
	Now            time.Time
}

type hotSearchID struct {
	ID        string
	CreatedAt time.Time
}

type hotSearchScanResult struct {
	Available        bool
	IDs              []hotSearchID
	Keywords         []string
	StartAt          string
	EndAt            string
	Limit            int
	Truncated        bool
	ScannedFileCount int
	Message          string
}

type hotSearchSearcher interface {
	Search(context.Context, HotSearchInput) hotSearchScanResult
}

type hotSearchScanner struct {
	root string
	slot chan struct{}
}

type hotSearchFile struct {
	path        string
	bucketStart time.Time
	modifiedAt  time.Time
}

type hotSearchFileListing struct {
	files     []hotSearchFile
	missing   bool
	truncated bool
}

type hotSearchLine struct {
	AuditLogID string `json:"auditLogId"`
	CreatedAt  string `json:"createdAt"`
	TraceID    string `json:"traceId"`
	Text       string `json:"text"`
}

func newHotSearchScanner(root string) *hotSearchScanner {
	return &hotSearchScanner{root: strings.TrimSpace(root), slot: make(chan struct{}, 1)}
}

func (s *hotSearchScanner) Search(ctx context.Context, input HotSearchInput) hotSearchScanResult {
	keywords, shortKeyword := normalizeHotSearchKeywords(input.Keywords)
	start, end := normalizeHotSearchRange(input)
	result := hotSearchScanResult{
		Available: true, IDs: []hotSearchID{}, Keywords: keywords,
		StartAt: formatHotSearchTime(start), EndAt: formatHotSearchTime(end), Limit: normalizeHotSearchLimit(input),
	}
	if len(keywords) == 0 {
		if shortKeyword {
			result.Message = "审计内容搜索关键字至少需要 2 个字符，请输入更具体的关键字。"
		} else {
			result.Message = "请输入要搜索的审计内容关键字"
		}
		return result
	}
	if s.root == "" {
		result.Available = false
		result.Message = "审计内容搜索镜像目录未配置，审计内容搜索不可用。"
		return result
	}
	select {
	case s.slot <- struct{}{}:
		defer func() { <-s.slot }()
	default:
		result.Available = false
		result.Message = "已有审计内容搜索正在运行，请稍后重试。"
		return result
	}

	searchCtx, cancel := context.WithTimeout(ctx, maxHotSearchDuration)
	defer cancel()
	listing, err := s.listFiles(searchCtx, start, end)
	if err != nil {
		result.Available = false
		result.Message = "审计内容搜索文件读取失败，审计内容搜索暂不可用。"
		return result
	}
	result.Truncated = listing.truncated
	if listing.missing || len(listing.files) == 0 {
		if listing.truncated {
			result.Message = "审计内容搜索目录扫描超过安全边界，未发现可搜索文件"
		} else {
			result.Message = "最近 1 小时没有可搜索的审计内容"
		}
		return result
	}
	result.ScannedFileCount = len(listing.files)
	seen := make(map[string]struct{})
	matchCount := 0
	for _, file := range listing.files {
		stopped, err := s.scanFile(searchCtx, file.path, keywords, start, end, seen, &matchCount, &result)
		if err != nil {
			result.Available = false
			result.IDs = []hotSearchID{}
			result.Truncated = false
			result.ScannedFileCount = 0
			result.Message = "审计内容搜索文件读取失败，审计内容搜索暂不可用。"
			return result
		}
		if stopped {
			result.Truncated = true
			break
		}
	}
	sort.Slice(result.IDs, func(i, j int) bool {
		if !result.IDs[i].CreatedAt.Equal(result.IDs[j].CreatedAt) {
			return result.IDs[i].CreatedAt.After(result.IDs[j].CreatedAt)
		}
		return result.IDs[i].ID < result.IDs[j].ID
	})
	if len(result.IDs) > result.Limit {
		result.Truncated = true
		result.IDs = result.IDs[:result.Limit]
	}
	if len(result.IDs) == 0 {
		if result.Truncated {
			result.Message = "审计内容搜索超过安全边界；没有匹配的审计内容"
		} else {
			result.Message = "没有匹配的审计内容"
		}
	} else if result.Truncated {
		result.Message = "结果超过安全上限，已按最新优先截断显示"
	}
	return result
}

func (s *hotSearchScanner) listFiles(ctx context.Context, start, end time.Time) (hotSearchFileListing, error) {
	directory, err := os.Open(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return hotSearchFileListing{missing: true}, nil
	}
	if err != nil {
		return hotSearchFileListing{}, err
	}
	defer directory.Close()
	listing := hotSearchFileListing{files: make([]hotSearchFile, 0, 24)}
	scanned := 0
	for scanned < maxHotSearchFiles {
		if err := ctx.Err(); err != nil {
			listing.truncated = true
			break
		}
		entries, readErr := directory.ReadDir(min(100, maxHotSearchFiles-scanned))
		for _, entry := range entries {
			scanned++
			if !entry.Type().IsRegular() {
				continue
			}
			bucket, ok := parseHotSearchFileName(entry.Name())
			if !ok || bucket.Add(time.Hour).Before(start) || bucket.After(end) {
				continue
			}
			info, infoErr := entry.Info()
			if errors.Is(infoErr, os.ErrNotExist) {
				continue
			}
			if infoErr != nil {
				return hotSearchFileListing{}, infoErr
			}
			if info.Size() <= 0 {
				continue
			}
			listing.files = append(listing.files, hotSearchFile{path: filepath.Join(s.root, entry.Name()), bucketStart: bucket, modifiedAt: info.ModTime()})
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return hotSearchFileListing{}, readErr
		}
	}
	if scanned == maxHotSearchFiles && !listing.truncated {
		if err := ctx.Err(); err != nil {
			listing.truncated = true
		} else {
			entries, readErr := directory.ReadDir(1)
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return hotSearchFileListing{}, readErr
			}
			listing.truncated = len(entries) > 0
		}
	}
	sort.Slice(listing.files, func(i, j int) bool {
		if !listing.files[i].bucketStart.Equal(listing.files[j].bucketStart) {
			return listing.files[i].bucketStart.After(listing.files[j].bucketStart)
		}
		return listing.files[i].modifiedAt.After(listing.files[j].modifiedAt)
	})
	return listing, nil
}

func (s *hotSearchScanner) scanFile(ctx context.Context, path string, keywords []string, start, end time.Time, seen map[string]struct{}, matchCount *int, result *hotSearchScanResult) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	position := info.Size()
	suffix := make([]byte, 0, hotSearchReadBlockBytes)
	droppingOversized := false
	for position > 0 {
		if err := ctx.Err(); err != nil {
			return true, nil
		}
		blockSize := int64(hotSearchReadBlockBytes)
		if position < blockSize {
			blockSize = position
		}
		position -= blockSize
		block := make([]byte, int(blockSize))
		read, readErr := file.ReadAt(block, position)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return false, readErr
		}
		block = block[:read]
		segmentEnd := len(block)
		for index := len(block) - 1; index >= 0; index-- {
			if block[index] != '\n' {
				continue
			}
			segment := block[index+1 : segmentEnd]
			if droppingOversized {
				droppingOversized = false
				result.Truncated = true
			} else {
				line, oversized := prependHotSearchFragment(segment, suffix)
				if oversized {
					result.Truncated = true
				} else if s.acceptHotSearchLine(line, keywords, start, end, seen, matchCount, result) {
					return true, nil
				}
			}
			suffix = suffix[:0]
			segmentEnd = index
		}
		prefix := block[:segmentEnd]
		if droppingOversized {
			continue
		}
		combined, oversized := prependHotSearchFragment(prefix, suffix)
		if oversized {
			droppingOversized = true
			suffix = suffix[:0]
			continue
		}
		suffix = combined
	}
	if droppingOversized {
		result.Truncated = true
		return false, nil
	}
	if len(suffix) > 0 && s.acceptHotSearchLine(suffix, keywords, start, end, seen, matchCount, result) {
		return true, nil
	}
	return false, nil
}

func prependHotSearchFragment(prefix, suffix []byte) ([]byte, bool) {
	if len(prefix) > maxHotSearchLineBytes-len(suffix) {
		return nil, true
	}
	line := make([]byte, len(prefix)+len(suffix))
	copy(line, prefix)
	copy(line[len(prefix):], suffix)
	return line, false
}

func (s *hotSearchScanner) acceptHotSearchLine(line []byte, keywords []string, start, end time.Time, seen map[string]struct{}, matchCount *int, result *hotSearchScanResult) bool {
	if len(line) == 0 {
		return false
	}
	item, matched, truncated := matchHotSearchLine(line, keywords)
	if truncated {
		result.Truncated = true
	}
	if !matched {
		return false
	}
	*matchCount++
	if len(item.AuditLogID) == 0 || len(item.AuditLogID) > maxHotSearchIDBytes {
		if len(item.AuditLogID) > maxHotSearchIDBytes {
			result.Truncated = true
		}
		return *matchCount >= maxHotSearchMatches
	}
	createdAt, parseErr := time.Parse(time.RFC3339Nano, item.CreatedAt)
	_, duplicate := seen[item.AuditLogID]
	if parseErr == nil && !createdAt.Before(start) && !createdAt.After(end) && !duplicate {
		seen[item.AuditLogID] = struct{}{}
		result.IDs = append(result.IDs, hotSearchID{ID: item.AuditLogID, CreatedAt: createdAt})
	}
	return *matchCount >= maxHotSearchMatches
}

func matchHotSearchLine(line []byte, keywords []string) (hotSearchLine, bool, bool) {
	oversizedSerializedLine := utf8.RuneCount(line) > maxHotSearchLineRunes
	if !oversizedSerializedLine && !hotSearchTextMatches(string(line), keywords) {
		return hotSearchLine{}, false, false
	}
	var item hotSearchLine
	if json.Unmarshal(line, &item) != nil {
		return hotSearchLine{}, false, oversizedSerializedLine
	}
	if !oversizedSerializedLine {
		return item, true, false
	}
	searchable := strings.Join([]string{item.AuditLogID, item.CreatedAt, item.TraceID, item.Text}, " ")
	if utf8.RuneCountInString(searchable) > maxHotSearchLineRunes {
		return hotSearchLine{}, false, true
	}
	return item, hotSearchTextMatches(searchable, keywords), false
}

func hotSearchTextMatches(value string, keywords []string) bool {
	value = strings.ToLower(value)
	for _, keyword := range keywords {
		if strings.Contains(value, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

func parseHotSearchFileName(name string) (time.Time, bool) {
	const prefix, suffix = "audit-hot-", ".ndjson"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return time.Time{}, false
	}
	value := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	if len(value) != 10 {
		return time.Time{}, false
	}
	parsed, err := time.ParseInLocation("2006010215", value, time.UTC)
	return parsed, err == nil
}

func normalizeHotSearchKeywords(values []string) ([]string, bool) {
	result := make([]string, 0, min(len(values), maxHotSearchKeywords))
	seen := make(map[string]struct{})
	short := false
	for _, raw := range values {
		value := trim(raw)
		if value == "" {
			continue
		}
		if utf8.RuneCountInString(value) > maxHotSearchKeywordRunes {
			value = string([]rune(value)[:maxHotSearchKeywordRunes])
		}
		if utf8.RuneCountInString(value) < minHotSearchKeywordRunes {
			short = true
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
		if len(result) == maxHotSearchKeywords {
			break
		}
	}
	return result, short
}

func normalizeHotSearchLimit(input HotSearchInput) int {
	if !input.LimitProvided && input.Limit == 0 {
		return defaultHotSearchLimit
	}
	return min(max(input.Limit, 1), maxHotSearchLimit)
}

func normalizeHotSearchRange(input HotSearchInput) (time.Time, time.Time) {
	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	end := parseHotSearchTime(input.EndAt, now)
	if end.After(now) {
		end = now
	}
	earliest := end.Add(-hotSearchWindow)
	start := parseHotSearchTime(input.StartAt, earliest)
	if start.Before(earliest) || start.After(end) {
		start = earliest
	}
	return start, end
}

func parseHotSearchTime(value string, fallback time.Time) time.Time {
	text := trim(value)
	if text == "" {
		return fallback
	}
	isoText := normalizeHotSearchISODateSeparators(text)
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04Z07:00",
		"2006-01-02T15:04:05.999999999Z0700",
		"2006-01-02T15:04Z0700",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04Z07:00",
		"2006-01-02 15:04:05.999999999Z0700",
		"2006-01-02 15:04Z0700",
		time.RFC1123Z,
		time.RFC1123,
		time.RFC850,
	} {
		if parsed, err := time.Parse(layout, isoText); err == nil {
			return parsed.UTC()
		}
	}
	for _, layout := range []string{"2006", "2006-01", time.DateOnly} {
		if parsed, err := time.ParseInLocation(layout, text, time.UTC); err == nil {
			return parsed.UTC()
		}
	}
	if parsed, err := time.ParseInLocation(time.ANSIC, text, time.Local); err == nil {
		return parsed.UTC()
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04",
		"2006/01/02 15:04:05.999999999",
		"2006/01/02 15:04",
	} {
		if parsed, err := time.ParseInLocation(layout, isoText, time.Local); err == nil {
			return parsed.UTC()
		}
	}
	return fallback
}

func normalizeHotSearchISODateSeparators(text string) string {
	bytes := []byte(text)
	if len(bytes) > len("2006-01-02") && bytes[len("2006-01-02")] == 't' {
		bytes[len("2006-01-02")] = 'T'
	}
	if len(bytes) > 0 && bytes[len(bytes)-1] == 'z' {
		bytes[len(bytes)-1] = 'Z'
	}
	return string(bytes)
}

func formatHotSearchTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}
