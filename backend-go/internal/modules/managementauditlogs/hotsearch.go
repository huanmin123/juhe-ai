package managementauditlogs

import (
	"bufio"
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
	maxHotSearchFiles        = 2_000
	maxHotSearchMatches      = 2_000
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

type hotSearchLine struct {
	AuditLogID string `json:"auditLogId"`
	CreatedAt  string `json:"createdAt"`
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
	files, missing, err := s.listFiles(searchCtx, start, end)
	if err != nil {
		result.Available = false
		result.Message = "审计内容搜索文件读取失败，审计内容搜索暂不可用。"
		return result
	}
	if missing || len(files) == 0 {
		result.Message = "最近 1 小时没有可搜索的审计内容"
		return result
	}
	result.ScannedFileCount = len(files)
	seen := make(map[string]struct{})
	matchCount := 0
	for _, file := range files {
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
		result.Message = "没有匹配的审计内容"
	} else if result.Truncated {
		result.Message = "结果超过安全上限，已按最新优先截断显示"
	}
	return result
}

func (s *hotSearchScanner) listFiles(ctx context.Context, start, end time.Time) ([]hotSearchFile, bool, error) {
	directory, err := os.Open(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer directory.Close()
	files := make([]hotSearchFile, 0, 24)
	scanned := 0
	for scanned < maxHotSearchFiles {
		if err := ctx.Err(); err != nil {
			return nil, false, err
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
			if infoErr != nil || info.Size() <= 0 {
				continue
			}
			files = append(files, hotSearchFile{path: filepath.Join(s.root, entry.Name()), bucketStart: bucket, modifiedAt: info.ModTime()})
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, false, readErr
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if !files[i].bucketStart.Equal(files[j].bucketStart) {
			return files[i].bucketStart.After(files[j].bucketStart)
		}
		return files[i].modifiedAt.After(files[j].modifiedAt)
	})
	return files, false, nil
}

func (s *hotSearchScanner) scanFile(ctx context.Context, path string, keywords []string, start, end time.Time, seen map[string]struct{}, matchCount *int, result *hotSearchScanResult) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, maxHotSearchLineBytes+1)
	for {
		if err := ctx.Err(); err != nil {
			return true, nil
		}
		line, oversized, readErr := readBoundedHotSearchLine(reader)
		if !oversized && len(line) > 0 && hotSearchLineMatches(line, keywords) {
			*matchCount++
			var item hotSearchLine
			if json.Unmarshal(line, &item) == nil && item.AuditLogID != "" {
				createdAt, parseErr := time.Parse(time.RFC3339Nano, item.CreatedAt)
				_, duplicate := seen[item.AuditLogID]
				if parseErr == nil && !createdAt.Before(start) && !createdAt.After(end) && !duplicate {
					seen[item.AuditLogID] = struct{}{}
					result.IDs = append(result.IDs, hotSearchID{ID: item.AuditLogID, CreatedAt: createdAt})
				}
			}
			if *matchCount >= maxHotSearchMatches {
				return true, nil
			}
		}
		if errors.Is(readErr, io.EOF) {
			return false, nil
		}
		if readErr != nil {
			return false, readErr
		}
	}
}

func readBoundedHotSearchLine(reader *bufio.Reader) ([]byte, bool, error) {
	line, err := reader.ReadSlice('\n')
	if !errors.Is(err, bufio.ErrBufferFull) {
		if len(line) > maxHotSearchLineBytes {
			return nil, true, err
		}
		return line, false, err
	}
	for errors.Is(err, bufio.ErrBufferFull) {
		_, err = reader.ReadSlice('\n')
	}
	return nil, true, err
}

func hotSearchLineMatches(line []byte, keywords []string) bool {
	if utf8.RuneCount(line) > maxHotSearchLineRunes {
		return false
	}
	value := strings.ToLower(string(line))
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
	parsed, err := time.Parse(time.RFC3339Nano, trim(value))
	if err != nil {
		return fallback
	}
	return parsed.UTC()
}

func formatHotSearchTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}
