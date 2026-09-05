package logreads

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// ---------------------------------------------------------------------------
// Runtime log grep family (runtime-log-grep.service.ts +
// runtime-log-grep-normalizers.ts): /grep-options, /grep and /grep-detail.
//
// Node scans the process file logs with ripgrep; this port reads the same
// JUHE_AI_LOG_DIR files directly with the identical fixed-string,
// case-insensitive, all-keywords contract and the same caps (newest
// maxFiles .log files, 3-day default / 7-day max window, 10 keywords of at
// least 3 runes, 20k-char lines, 2000 parsed matches, 15s deadline, single
// concurrent search). Documented divergences: file start times use mtime
// (Go has no portable birthtime), a missing log directory answers the
// "没有可搜索的日志文件" contract instead of Node's opendir crash, and the
// rg-specific failure message collapses into the generic scan-failure one.
// ---------------------------------------------------------------------------

const (
	grepMinKeywordRunes    = 3
	grepMaxKeywordRunes    = 128
	grepMaxKeywords        = 10
	grepDefaultLimit       = 100
	grepMaxLimit           = 100
	grepDefaultRangeDays   = 3
	grepMaxRangeDays       = 7
	grepMaxLineLength      = 20_000
	grepMaxLineBytes       = 20_000 * 4 // 20k runes never exceed 4 bytes each
	grepMaxDetailLineBytes = grepMaxLineLength * 4
	grepMaxPreviewText     = 1_000
	grepMaxIdentityText    = 256
	grepMaxMatchEvents     = 2_000
	grepMaxSearchMs        = 15 * time.Second
	grepMaxDirScanEntries  = 10_000
	grepMaxDirScanMs       = 2 * time.Second
	grepMaxConcurrent      = 1
	grepDayMillis          = int64(24 * 60 * 60 * 1000)
)

// RuntimeLogGrepConfig mirrors the runtimeConfig.log.* surface the grep
// family reads (JUHE_AI_LOG_FILE_ENABLED / JUHE_AI_LOG_DIR /
// JUHE_AI_LOG_MAX_FILES / JUHE_AI_LOG_RETENTION_DAYS).
type RuntimeLogGrepConfig struct {
	FileEnabled   bool
	Directory     string
	MaxFiles      int
	RetentionDays int
}

// RuntimeLogGrep is the file-scanner service behind the grep routes.
type RuntimeLogGrep struct {
	cfg    RuntimeLogGrepConfig
	active atomic.Int64
	// Now pins the clock for tests; nil means time.Now.
	Now func() time.Time
}

// NewRuntimeLogGrep clamps the config bounds (Node numberConfig semantics)
// and returns the service. An empty directory keeps the service mounted but
// reports the file-logging-disabled contract.
func NewRuntimeLogGrep(cfg RuntimeLogGrepConfig) *RuntimeLogGrep {
	cfg.Directory = strings.TrimSpace(cfg.Directory)
	if cfg.MaxFiles <= 0 {
		cfg.MaxFiles = 500
	}
	if cfg.RetentionDays <= 0 {
		cfg.RetentionDays = 30
	}
	cfg.MaxFiles = min(cfg.MaxFiles, 500)
	cfg.RetentionDays = min(cfg.RetentionDays, 30)
	if cfg.Directory == "" {
		cfg.FileEnabled = false
	}
	return &RuntimeLogGrep{cfg: cfg}
}

func (g *RuntimeLogGrep) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// RuntimeLogGrepItem mirrors RuntimeLogGrepItem.
type RuntimeLogGrepItem struct {
	ID           string `json:"id"`
	FileName     string `json:"fileName"`
	LineNumber   int64  `json:"lineNumber"`
	Time         string `json:"time"`
	Level        string `json:"level"`
	TraceID      string `json:"traceId,omitempty"`
	Event        string `json:"event,omitempty"`
	Message      string `json:"message,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

// RuntimeLogGrepDetail mirrors RuntimeLogGrepDetail.
type RuntimeLogGrepDetail struct {
	File string `json:"file"`
	Line string `json:"line"`
}

// RuntimeLogGrepRuntime mirrors RuntimeLogGrepRuntime (the /grep-options DTO).
type RuntimeLogGrepRuntime struct {
	EarliestFileTime      string `json:"earliestFileTime,omitempty"`
	DefaultStartAt        string `json:"defaultStartAt"`
	DefaultEndAt          string `json:"defaultEndAt"`
	DefaultRangeDays      int    `json:"defaultRangeDays"`
	MaxRangeDays          int    `json:"maxRangeDays"`
	FileRetentionDays     int    `json:"fileRetentionDays"`
	ActiveSearchCount     int64  `json:"activeSearchCount"`
	MaxConcurrentSearches int    `json:"maxConcurrentSearches"`
}

// RuntimeLogGrepResult mirrors RuntimeLogGrepResult.
type RuntimeLogGrepResult struct {
	Available        bool                 `json:"available"`
	ElapsedMs        int64                `json:"elapsedMs"`
	Keywords         []string             `json:"keywords"`
	StartAt          string               `json:"startAt"`
	EndAt            string               `json:"endAt"`
	DefaultRangeDays int                  `json:"defaultRangeDays"`
	MaxRangeDays     int                  `json:"maxRangeDays"`
	Items            []RuntimeLogGrepItem `json:"items"`
	Limit            int                  `json:"limit"`
	Truncated        bool                 `json:"truncated"`
	ScannedFileCount int                  `json:"scannedFileCount"`
	Message          string               `json:"message,omitempty"`
}

// RuntimeLogGrepOptions mirrors RuntimeLogGrepOptions.
type RuntimeLogGrepOptions struct {
	Keywords []string
	Limit    *int
	StartAt  string
	EndAt    string
}

type grepLogFile struct {
	path     string
	fileName string
	size     int64
	mtimeMs  int64
	order    int
}

type grepTimeRange struct {
	startMs  int64
	endMs    int64
	startAt  string
	endAt    string
	adjusted bool
}

// normalizeGrepKeywords mirrors normalizeGrepKeywords: split on whitespace
// and the ASCII/fullwidth comma+semicolon separators, cap keywords at 128
// runes, drop keywords shorter than three runes (counted), dedupe by
// lowercase, keep at most ten.
func normalizeGrepKeywords(values []string) (keywords []string, shortCount int) {
	seen := make(map[string]bool)
	keywords = make([]string, 0, len(values))
	for _, value := range values {
		for _, part := range strings.FieldsFunc(value, func(r rune) bool {
			return unicode.IsSpace(r) || r == ',' || r == ';' || r == '，' || r == '；'
		}) {
			keyword := strings.TrimSpace(part)
			if keyword == "" {
				continue
			}
			if runes := []rune(keyword); len(runes) > grepMaxKeywordRunes {
				keyword = string(runes[:grepMaxKeywordRunes])
			}
			if utf8.RuneCountInString(keyword) < grepMinKeywordRunes {
				shortCount++
				continue
			}
			dedupeKey := strings.ToLower(keyword)
			if seen[dedupeKey] {
				continue
			}
			seen[dedupeKey] = true
			keywords = append(keywords, keyword)
			if len(keywords) >= grepMaxKeywords {
				return keywords, shortCount
			}
		}
	}
	return keywords, shortCount
}

// normalizeGrepLimit mirrors normalizeGrepLimit.
func normalizeGrepLimit(value *int) int {
	if value == nil {
		return grepDefaultLimit
	}
	return min(grepMaxLimit, max(1, *value))
}

// parseGrepInstantMillis mirrors parseTimeMs with its dedicated error text.
func parseGrepInstantMillis(value string) (int64, error) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return 0, readParamErrorf("运行日志时间范围必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	return parsed.UnixMilli(), nil
}

// normalizeGrepTimeRange mirrors normalizeGrepTimeRange.
func (g *RuntimeLogGrep) normalizeGrepTimeRange(startAt, endAt string, files []grepLogFile) (grepTimeRange, error) {
	nowMs := g.now().UnixMilli()
	var earliestFileMs, latestFileMs *int64
	if len(files) > 0 {
		earliest, latest := files[0].mtimeMs, files[0].mtimeMs
		for _, file := range files[1:] {
			earliest = min(earliest, file.mtimeMs)
			latest = max(latest, file.mtimeMs)
		}
		earliestFileMs, latestFileMs = &earliest, &latest
	}
	adjusted := false
	var requestedEndMs *int64
	if strings.TrimSpace(endAt) != "" {
		parsed, err := parseGrepInstantMillis(endAt)
		if err != nil {
			return grepTimeRange{}, err
		}
		requestedEndMs = &parsed
	}
	endMs := nowMs
	if requestedEndMs != nil {
		endMs = *requestedEndMs
	}
	if endMs > nowMs {
		endMs = nowMs
		adjusted = true
	}
	if requestedEndMs == nil && latestFileMs != nil && endMs < *latestFileMs {
		endMs = *latestFileMs
		adjusted = true
	}
	if earliestFileMs != nil && endMs < *earliestFileMs {
		endMs = *earliestFileMs
		adjusted = true
	}
	var startMs int64
	if strings.TrimSpace(startAt) != "" {
		parsed, err := parseGrepInstantMillis(startAt)
		if err != nil {
			return grepTimeRange{}, err
		}
		startMs = parsed
	} else {
		startMs = endMs - grepDefaultRangeDays*grepDayMillis
	}
	if earliestFileMs != nil && startMs < *earliestFileMs {
		startMs = *earliestFileMs
		adjusted = true
	}
	if startMs > endMs {
		startMs = endMs - grepDefaultRangeDays*grepDayMillis
		if earliestFileMs != nil {
			startMs = max(startMs, *earliestFileMs)
		}
		adjusted = true
	}
	if endMs-startMs > grepMaxRangeDays*grepDayMillis {
		startMs = endMs - grepMaxRangeDays*grepDayMillis
		adjusted = true
	}
	if earliestFileMs != nil && startMs < *earliestFileMs {
		startMs = *earliestFileMs
	}
	return grepTimeRange{
		startMs:  startMs,
		endMs:    endMs,
		startAt:  millisToISO(startMs),
		endAt:    millisToISO(endMs),
		adjusted: adjusted,
	}, nil
}

// filterLogFilesByTimeRange mirrors filterLogFilesByTimeRange (empty and
// out-of-window files drop out).
func filterLogFilesByTimeRange(files []grepLogFile, timeRange grepTimeRange) []grepLogFile {
	searchable := make([]grepLogFile, 0, len(files))
	for _, file := range files {
		if file.size <= 0 {
			continue
		}
		if file.mtimeMs >= timeRange.startMs && file.mtimeMs <= timeRange.endMs {
			searchable = append(searchable, file)
		}
	}
	return searchable
}

// listLogFiles mirrors listLogFiles: newest maxFiles *.log files by mtime
// with the entry-count and deadline caps. The warning mirrors
// logFileListingWarning.
func (g *RuntimeLogGrep) listLogFiles() (files []grepLogFile, warning string, err error) {
	if !g.cfg.FileEnabled {
		return nil, "", nil
	}
	deadline := g.now().Add(grepMaxDirScanMs)
	entries, err := os.ReadDir(g.cfg.Directory)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, "", nil
		}
		return nil, "", err
	}
	retained := make([]grepLogFile, 0, 8)
	scanned := 0
	truncatedReason := ""
	for _, entry := range entries {
		if scanned >= grepMaxDirScanEntries {
			truncatedReason = "entry_limit"
			break
		}
		if g.now().After(deadline) {
			truncatedReason = "deadline"
			break
		}
		scanned++
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		path := filepath.Join(g.cfg.Directory, entry.Name())
		info, statErr := os.Stat(path)
		if statErr != nil {
			if errors.Is(statErr, fs.ErrNotExist) {
				continue
			}
			return nil, "", statErr
		}
		if !info.Mode().IsRegular() {
			continue
		}
		retained = retainNewestGrepFile(retained, grepLogFile{
			path:     path,
			fileName: entry.Name(),
			size:     info.Size(),
			mtimeMs:  info.ModTime().UnixMilli(),
		}, g.cfg.MaxFiles)
	}
	for index := range retained {
		retained[index].order = index
	}
	switch truncatedReason {
	case "deadline":
		warning = "日志目录扫描超过 2 秒，已只使用扫描到的最新日志文件"
	case "entry_limit":
		warning = "日志目录条目超过 10000 个，已只使用扫描到的最新日志文件"
	}
	return retained, warning, nil
}

// retainNewestGrepFile mirrors retainNewestLogFile: keep the maxFiles
// newest entries by mtime (desc).
func retainNewestGrepFile(files []grepLogFile, file grepLogFile, maxFiles int) []grepLogFile {
	insertIndex := len(files)
	for index := range files {
		if file.mtimeMs > files[index].mtimeMs {
			insertIndex = index
			break
		}
	}
	if insertIndex >= maxFiles {
		return files
	}
	files = append(files, grepLogFile{})
	copy(files[insertIndex+1:], files[insertIndex:])
	files[insertIndex] = file
	if len(files) > maxFiles {
		files = files[:maxFiles]
	}
	return files
}

// Search mirrors grepRuntimeLogFiles: every degradation is a 200 response
// with available:false or an explanatory message, matching the Node route.
func (g *RuntimeLogGrep) Search(ctx context.Context, options RuntimeLogGrepOptions) (RuntimeLogGrepResult, error) {
	started := g.now()
	keywords, shortCount := normalizeGrepKeywords(options.Keywords)
	limit := normalizeGrepLimit(options.Limit)
	build := func(timeRange grepTimeRange) RuntimeLogGrepResult {
		return RuntimeLogGrepResult{
			Keywords:         keywords,
			StartAt:          timeRange.startAt,
			EndAt:            timeRange.endAt,
			DefaultRangeDays: grepDefaultRangeDays,
			MaxRangeDays:     grepMaxRangeDays,
			Limit:            limit,
			Items:            []RuntimeLogGrepItem{},
		}
	}
	emptyRange, err := g.normalizeGrepTimeRange(options.StartAt, options.EndAt, nil)
	if err != nil {
		return RuntimeLogGrepResult{}, err
	}
	if len(keywords) == 0 {
		result := build(emptyRange)
		result.Available = true
		result.ElapsedMs = g.sinceMillis(started)
		if shortCount > 0 {
			result.Message = "grep 关键字至少需要 3 个字符，请输入更具体的关键字。"
		} else {
			result.Message = "请输入要搜索的关键字"
		}
		return result, nil
	}
	if !g.cfg.FileEnabled {
		result := build(emptyRange)
		result.ElapsedMs = g.sinceMillis(started)
		result.Message = "文件日志未启用，无法使用 grep 模式。"
		return result, nil
	}
	if !g.active.CompareAndSwap(0, 1) {
		result := build(emptyRange)
		result.ElapsedMs = g.sinceMillis(started)
		result.Message = "已有 grep 搜索正在运行，请稍后重试。"
		return result, nil
	}
	defer g.active.Add(-1)
	listing, listingWarning, err := g.listLogFiles()
	if err != nil {
		return RuntimeLogGrepResult{}, err
	}
	timeRange, err := g.normalizeGrepTimeRange(options.StartAt, options.EndAt, listing)
	if err != nil {
		return RuntimeLogGrepResult{}, err
	}
	warnings := make([]string, 0, 3)
	if listingWarning != "" {
		warnings = append(warnings, listingWarning)
	}
	if timeRange.adjusted {
		warnings = append(warnings, "grep 文件时间范围已自动调整，单次最多 7 天")
	}
	if shortCount > 0 {
		warnings = append(warnings, "已忽略少于 3 个字符的短关键字")
	}
	if len(listing) == 0 {
		result := build(timeRange)
		result.Available = true
		result.ElapsedMs = g.sinceMillis(started)
		messages := append(append([]string{}, warnings...), "没有可搜索的日志文件")
		result.Message = strings.Join(messages, "；")
		return result, nil
	}
	searchable := filterLogFilesByTimeRange(listing, timeRange)
	if len(searchable) == 0 {
		result := build(timeRange)
		result.Available = true
		result.ElapsedMs = g.sinceMillis(started)
		message := "当前文件时间范围内没有可搜索的日志文件"
		if timeRange.adjusted {
			message = "grep 文件时间范围已自动调整，单次最多 7 天；当前时间范围内没有可搜索的日志文件。"
		}
		messages := append(append([]string{}, warnings...), message)
		result.Message = strings.Join(messages, "；")
		return result, nil
	}
	items, matchedCount, anyPrimaryMatch, stoppedReason, err := g.scanLogFiles(ctx, searchable, keywords, limit, started)
	if err != nil {
		result := build(timeRange)
		result.ElapsedMs = g.sinceMillis(started)
		result.Message = "日志文件扫描失败，grep 模式暂不可用。"
		return result, nil
	}
	truncated := matchedCount > limit || stoppedReason != ""
	result := build(timeRange)
	result.Available = true
	result.ElapsedMs = g.sinceMillis(started)
	result.Items = items
	result.Truncated = truncated
	result.ScannedFileCount = len(searchable)
	messages := append([]string{}, warnings...)
	if truncated {
		messages = append(messages, "结果超过 "+strconv.Itoa(limit)+" 行，已按最新优先截断显示")
	}
	switch stoppedReason {
	case "match_parse_limit":
		messages = append(messages, "grep 命中行超过安全解析上限 2000，已提前停止以保护 DB service 事件循环")
	case "timeout":
		messages = append(messages, "grep 搜索超过 15 秒，已提前停止以保护 DB service 事件循环")
	default:
		if !anyPrimaryMatch {
			messages = append(messages, "没有匹配的日志行")
		}
	}
	result.Message = strings.Join(messages, "；")
	return result, nil
}

func (g *RuntimeLogGrep) sinceMillis(started time.Time) int64 {
	return max(int64(0), g.now().Sub(started).Milliseconds())
}

// orderedGrepItem mirrors OrderedRuntimeLogGrepItem: the sort keys travel
// with the item and are stripped before responding.
type orderedGrepItem struct {
	item       RuntimeLogGrepItem
	fileOrder  int
	sortTimeMs int64
}

// compareGrepItems mirrors compareGrepItems: newest first, then file order,
// then the higher line number first.
func compareGrepItems(left, right orderedGrepItem) bool {
	if left.sortTimeMs != right.sortTimeMs {
		return left.sortTimeMs > right.sortTimeMs
	}
	if left.fileOrder != right.fileOrder {
		return left.fileOrder < right.fileOrder
	}
	return left.item.LineNumber > right.item.LineNumber
}

// insertLatestGrepItem mirrors insertLatestGrepItem: stable insert-sort
// with the limit bound.
func insertLatestGrepItem(items []orderedGrepItem, item orderedGrepItem, limit int) []orderedGrepItem {
	items = append(items, item)
	sort.SliceStable(items, func(left, right int) bool {
		return compareGrepItems(items[left], items[right])
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items
}

// scanLogFiles reads every searchable file line by line: a line matches
// when (case-insensitively) it contains all keywords, stays under the
// 20k-char cap and is not a runtime-log search request line. The top limit
// newest items are kept. anyPrimaryMatch approximates the Node rg exit
// state ("the primary keyword appeared at least once").
func (g *RuntimeLogGrep) scanLogFiles(ctx context.Context, files []grepLogFile, keywords []string, limit int, started time.Time) (items []RuntimeLogGrepItem, matchedCount int, anyPrimaryMatch bool, stoppedReason string, err error) {
	lowered := make([]string, 0, len(keywords))
	for _, keyword := range keywords {
		lowered = append(lowered, strings.ToLower(keyword))
	}
	// selectPrimaryKeyword: the longest keyword drives the rg prefilter.
	primary := ""
	for _, keyword := range lowered {
		if len(keyword) > len(primary) {
			primary = keyword
		}
	}
	deadline := started.Add(grepMaxSearchMs)
	ordered := make([]orderedGrepItem, 0, limit)
	lineCounter := 0
scan:
	for _, file := range files {
		if stoppedReason != "" {
			break
		}
		fileHandle, openErr := os.Open(file.path)
		if openErr != nil {
			return nil, 0, false, "", openErr
		}
		reader := bufio.NewReaderSize(fileHandle, 64*1024)
		lineNumber := int64(0)
		for {
			if lineCounter%1024 == 0 {
				if ctxErr := ctx.Err(); ctxErr != nil {
					_ = fileHandle.Close()
					return nil, 0, false, "", ctxErr
				}
				if g.now().After(deadline) {
					stoppedReason = "timeout"
					_ = fileHandle.Close()
					break scan
				}
			}
			lineCounter++
			raw, _, cleanEOF, tooLong, readErr := readBoundedHotLine(reader, grepMaxLineBytes)
			if readErr != nil {
				_ = fileHandle.Close()
				return nil, 0, false, "", readErr
			}
			lineNumber++
			if tooLong {
				if cleanEOF {
					break
				}
				continue
			}
			if raw == nil {
				break
			}
			line := strings.TrimSuffix(string(raw), "\r")
			if utf8.RuneCountInString(line) <= grepMaxLineLength &&
				lineContainsAllKeywords(line, lowered) &&
				!isRuntimeLogSearchRequestLine(line) {
				matchedCount++
				if strings.Contains(strings.ToLower(line), primary) {
					anyPrimaryMatch = true
				}
				fields := runtimeLogFieldsFromLine(line)
				sortTimeMs, ok := grepSortTimeMs(fields.time)
				if !ok {
					sortTimeMs = file.mtimeMs
				}
				ordered = insertLatestGrepItem(ordered, orderedGrepItem{
					item: RuntimeLogGrepItem{
						ID:           runtimeLogGrepItemID(file.fileName, lineNumber, line),
						FileName:     file.fileName,
						LineNumber:   lineNumber,
						Time:         fields.time,
						Level:        fields.level,
						TraceID:      fields.traceID,
						Event:        fields.event,
						Message:      fields.message,
						ErrorMessage: fields.errorMessage,
					},
					fileOrder:  file.order,
					sortTimeMs: sortTimeMs,
				}, limit)
				if matchedCount >= grepMaxMatchEvents {
					stoppedReason = "match_parse_limit"
					_ = fileHandle.Close()
					break scan
				}
			}
			if cleanEOF {
				break
			}
		}
		_ = fileHandle.Close()
	}
	items = make([]RuntimeLogGrepItem, 0, len(ordered))
	for _, entry := range ordered {
		items = append(items, entry.item)
	}
	return items, matchedCount, anyPrimaryMatch, stoppedReason, nil
}

func lineContainsAllKeywords(line string, loweredKeywords []string) bool {
	searchable := strings.ToLower(line)
	for _, keyword := range loweredKeywords {
		if !strings.Contains(searchable, keyword) {
			return false
		}
	}
	return true
}

// isRuntimeLogSearchRequestLine mirrors isRuntimeLogSearchRequestLine: the
// grep API's own request-completion records never match themselves.
func isRuntimeLogSearchRequestLine(line string) bool {
	if utf8.RuneCountInString(line) > grepMaxLineLength {
		return strings.Contains(line, "/__aisys__/api/runtime-logs")
	}
	var parsed struct {
		Event       string `json:"event"`
		OriginalURL string `json:"originalUrl"`
		Path        string `json:"path"`
	}
	if err := json.Unmarshal([]byte(line), &parsed); err != nil {
		return false
	}
	if parsed.Event != "http_request_completed" && parsed.Event != "http_request_closed" {
		return false
	}
	return isRuntimeLogSearchPath(parsed.OriginalURL) || isRuntimeLogSearchPath(parsed.Path)
}

func isRuntimeLogSearchPath(value string) bool {
	if value == "" {
		return false
	}
	path := value
	if index := strings.IndexByte(path, '?'); index >= 0 {
		path = path[:index]
	}
	path = strings.TrimRight(path, "/")
	return path == "/__aisys__/api/runtime-logs" || strings.HasPrefix(path, "/__aisys__/api/runtime-logs/")
}

// runtimeLogGrepItemID mirrors runtimeLogGrepItemId: sha256 over
// fileName \0 lineNumber \0 line.
func runtimeLogGrepItemID(fileName string, lineNumber int64, line string) string {
	digest := sha256.Sum256([]byte(fileName + "\x00" + strconv.FormatInt(lineNumber, 10) + "\x00" + line))
	return hex.EncodeToString(digest[:])
}

// trimGrepText mirrors trimLine: cut to length runes and mark with "...".
func trimGrepText(value string, length int) string {
	if utf8.RuneCountInString(value) > length {
		runes := []rune(value)
		return string(runes[:length]) + "..."
	}
	return value
}

type grepItemFields struct {
	time, level, traceID, event, message, errorMessage string
}

// runtimeLogFieldsFromLine mirrors runtimeLogFieldsFromLine (JSON record
// fields; the raw preview fallback keeps the trimLine shape).
func runtimeLogFieldsFromLine(line string) grepItemFields {
	rawLine := trimGrepText(line, grepMaxPreviewText)
	if utf8.RuneCountInString(line) > grepMaxLineLength {
		return grepItemFields{time: "", level: "info", message: rawLine}
	}
	var parsed struct {
		Time         any    `json:"time"`
		Level        any    `json:"level"`
		TraceID      string `json:"traceId"`
		Event        string `json:"event"`
		Msg          string `json:"msg"`
		Message      string `json:"message"`
		ErrorMessage string `json:"errorMessage"`
		Err          *struct {
			Message string `json:"message"`
		} `json:"err"`
	}
	if err := json.Unmarshal([]byte(line), &parsed); err != nil {
		return grepItemFields{time: "", level: "info", message: rawLine}
	}
	message := grepOptionalText(parsed.Msg)
	if message == "" {
		message = grepOptionalText(parsed.Message)
	}
	errorMessage := grepOptionalText(parsed.ErrorMessage)
	if errorMessage == "" && parsed.Err != nil {
		errorMessage = grepOptionalText(parsed.Err.Message)
	}
	return grepItemFields{
		time:         grepTimeValue(parsed.Time),
		level:        grepNormalizeLevel(parsed.Level),
		traceID:      trimGrepText(grepOptionalText(parsed.TraceID), grepMaxIdentityText),
		event:        trimGrepText(grepOptionalText(parsed.Event), grepMaxIdentityText),
		message:      message,
		errorMessage: errorMessage,
	}
}

// grepOptionalText mirrors stringValue: trimmed non-empty text only.
func grepOptionalText(value string) string {
	return strings.TrimSpace(value)
}

// grepTimeValue mirrors runtimeLogTimeValue.
func grepTimeValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return millisToISO(int64(typed))
	default:
		return ""
	}
}

// grepNormalizeLevel mirrors normalizeLevel (string passthrough, pino
// numeric bands otherwise, info fallback).
func grepNormalizeLevel(value any) string {
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) != "" {
			return strings.ToLower(strings.TrimSpace(typed))
		}
	case float64:
		switch {
		case typed >= 60:
			return "fatal"
		case typed >= 50:
			return "error"
		case typed >= 40:
			return "warn"
		case typed >= 30:
			return "info"
		case typed >= 20:
			return "debug"
		default:
			return "trace"
		}
	}
	return "info"
}

// grepSortTimeMs mirrors parseRuntimeLogTimeMs: RFC3339 millis or false.
func grepSortTimeMs(value string) (int64, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return 0, false
	}
	return parsed.UnixMilli(), true
}

// Options mirrors getRuntimeLogGrepRuntime (the /grep-options DTO).
func (g *RuntimeLogGrep) Options() (RuntimeLogGrepRuntime, error) {
	files := []grepLogFile{}
	if g.cfg.FileEnabled {
		listing, _, err := g.listLogFiles()
		if err != nil {
			return RuntimeLogGrepRuntime{}, err
		}
		files = listing
	}
	timeRange, err := g.normalizeGrepTimeRange("", "", files)
	if err != nil {
		return RuntimeLogGrepRuntime{}, err
	}
	runtime := RuntimeLogGrepRuntime{
		DefaultStartAt:        timeRange.startAt,
		DefaultEndAt:          timeRange.endAt,
		DefaultRangeDays:      grepDefaultRangeDays,
		MaxRangeDays:          grepMaxRangeDays,
		FileRetentionDays:     g.cfg.RetentionDays,
		ActiveSearchCount:     g.active.Load(),
		MaxConcurrentSearches: grepMaxConcurrent,
	}
	if len(files) > 0 {
		earliest := files[0].mtimeMs
		for _, file := range files[1:] {
			earliest = min(earliest, file.mtimeMs)
		}
		runtime.EarliestFileTime = millisToISO(earliest)
	}
	return runtime, nil
}

// RuntimeLogGrepLookup mirrors RuntimeLogGrepDetailLookup.
type RuntimeLogGrepLookup struct {
	Status string // "ok" | "not_found" | "stale"
	Detail RuntimeLogGrepDetail
}

var grepDetailIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Detail mirrors getRuntimeLogGrepDetail: pin the exact line by recomputing
// its content hash; any drift reports stale.
func (g *RuntimeLogGrep) Detail(id, fileName string, lineNumber int64) RuntimeLogGrepLookup {
	id = strings.TrimSpace(id)
	fileName = strings.TrimSpace(fileName)
	if !grepDetailIDPattern.MatchString(id) || fileName == "" || lineNumber < 1 {
		return RuntimeLogGrepLookup{Status: "not_found"}
	}
	listing, _, err := g.listLogFiles()
	if err != nil {
		return RuntimeLogGrepLookup{Status: "not_found"}
	}
	var file *grepLogFile
	for index := range listing {
		if listing[index].fileName == fileName {
			file = &listing[index]
			break
		}
	}
	if file == nil {
		return RuntimeLogGrepLookup{Status: "not_found"}
	}
	status, line := readRuntimeLogLine(file.path, lineNumber)
	switch status {
	case "not_found", "too_large":
		return RuntimeLogGrepLookup{Status: "stale"}
	}
	if runtimeLogGrepItemID(file.fileName, lineNumber, line) != id {
		return RuntimeLogGrepLookup{Status: "stale"}
	}
	return RuntimeLogGrepLookup{Status: "ok", Detail: RuntimeLogGrepDetail{File: file.path, Line: line}}
}

// readRuntimeLogLine mirrors readRuntimeLogLine with maxDetailLineBytes.
func readRuntimeLogLine(path string, target int64) (string, string) {
	file, err := os.Open(path)
	if err != nil {
		return "not_found", ""
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64*1024)
	lineNumber := int64(0)
	for {
		raw, _, cleanEOF, tooLong, readErr := readBoundedHotLine(reader, grepMaxDetailLineBytes)
		if readErr != nil {
			return "not_found", ""
		}
		lineNumber++
		if tooLong {
			if cleanEOF {
				return "not_found", ""
			}
			continue
		}
		if raw == nil {
			return "not_found", ""
		}
		if lineNumber == target {
			return "found", strings.TrimSuffix(string(raw), "\r")
		}
		if cleanEOF {
			return "not_found", ""
		}
	}
}

// handleRuntimeLogGrepOptions mirrors GET /grep-options.
func (d *ReadsDeps) handleRuntimeLogGrepOptions(w http.ResponseWriter, r *http.Request) {
	runtime, err := d.Grep.Options()
	if readWriteStoreError(w, err) {
		return
	}
	kernel.WriteOK(w, runtime, "")
}

// handleRuntimeLogGrep mirrors GET /grep.
func (d *ReadsDeps) handleRuntimeLogGrep(w http.ResponseWriter, r *http.Request) {
	options, err := parseRuntimeLogGrepOptions(r)
	if readWriteStoreError(w, err) {
		return
	}
	result, err := d.Grep.Search(r.Context(), options)
	if readWriteStoreError(w, err) {
		return
	}
	kernel.WriteOK(w, result, "")
}

// parseRuntimeLogGrepOptions mirrors parseRuntimeLogGrepOptions.
func parseRuntimeLogGrepOptions(r *http.Request) (RuntimeLogGrepOptions, error) {
	startAt, endAt, err := readQueryDateTimeRange(r)
	if err != nil {
		return RuntimeLogGrepOptions{}, err
	}
	keywords := append([]string{}, r.URL.Query()["keywords"]...)
	return RuntimeLogGrepOptions{
		Keywords: keywords,
		Limit:    readQueryInt(r, "limit"),
		StartAt:  startAt,
		EndAt:    endAt,
	}, nil
}

// handleRuntimeLogGrepDetail mirrors GET /grep-detail: 400 on invalid
// anchors, 404 on unknown lines, 409 on rotated content.
func (d *ReadsDeps) handleRuntimeLogGrepDetail(w http.ResponseWriter, r *http.Request) {
	id := readQueryText(r, "id")
	fileName := readQueryText(r, "fileName")
	lineNumber := readQueryInt(r, "lineNumber")
	if id == "" || fileName == "" || lineNumber == nil || *lineNumber < 1 {
		kernel.WriteBadRequest(w, "grep 详情定位参数无效")
		return
	}
	lookup := d.Grep.Detail(id, fileName, int64(*lineNumber))
	switch lookup.Status {
	case "not_found":
		kernel.WriteNotFound(w, "grep 匹配行不存在")
		return
	case "stale":
		kernel.WriteError(w, http.StatusConflict, "日志文件已经轮转或内容发生变化，请重新搜索")
		return
	}
	kernel.WriteOK(w, lookup.Detail, "")
}
