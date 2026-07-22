package managementruntimeloggrep

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	defaultRangeDays          = 3
	maxRangeDays              = 7
	minKeywordLength          = 3
	maxKeywords               = 10
	maxKeywordLength          = 128
	defaultLimit              = 100
	maxLimit                  = 100
	maxLineLength             = 20_000
	maxRGJSONLineLength       = 128 * 1024 // Covers sixfold JSON escaping plus rg envelope overhead.
	maxParsedMatches          = 2_000
	maxConcurrentSearches     = 1
	maxDirectoryEntries       = 10_000
	maxDirectoryScanDuration  = 2 * time.Second
	maxRGSearchDuration       = 15 * time.Second
	maxRGCommandCharacters    = 24_000
	defaultRetentionDays      = 30
	maximumRetentionDays      = 30
	defaultMaximumFiles       = 500
	maximumFiles              = 500
	runtimeLogTimestampLayout = "2006-01-02T15:04:05.000Z07:00"
)

type Input struct {
	Keywords []string
	Limit    int
	StartAt  time.Time
	EndAt    time.Time
}

type Item struct {
	ID           string `json:"id"`
	File         string `json:"file"`
	FileName     string `json:"fileName"`
	LineNumber   int    `json:"lineNumber,omitempty"`
	Time         string `json:"time"`
	Level        string `json:"level"`
	TraceID      string `json:"traceId,omitempty"`
	Event        string `json:"event,omitempty"`
	Message      string `json:"message,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
	Line         string `json:"line"`

	sortTime time.Time
	fileRank int
}

type Result struct {
	Available        bool     `json:"available"`
	ElapsedMS        int64    `json:"elapsedMs"`
	Keywords         []string `json:"keywords"`
	StartAt          string   `json:"startAt"`
	EndAt            string   `json:"endAt"`
	DefaultRangeDays int      `json:"defaultRangeDays"`
	MaxRangeDays     int      `json:"maxRangeDays"`
	Items            []Item   `json:"items"`
	Limit            int      `json:"limit"`
	Truncated        bool     `json:"truncated"`
	ScannedFileCount int      `json:"scannedFileCount"`
	Message          string   `json:"message,omitempty"`
}

type Runtime struct {
	EarliestFileTime      string `json:"earliestFileTime,omitempty"`
	DefaultStartAt        string `json:"defaultStartAt"`
	DefaultEndAt          string `json:"defaultEndAt"`
	DefaultRangeDays      int    `json:"defaultRangeDays"`
	MaxRangeDays          int    `json:"maxRangeDays"`
	FileRetentionDays     int    `json:"fileRetentionDays"`
	ActiveSearchCount     int    `json:"activeSearchCount"`
	MaxConcurrentSearches int    `json:"maxConcurrentSearches"`
}

type Options struct {
	Directory     string
	FileEnabled   bool
	MaxFiles      int
	RetentionDays int
	RGPath        string
	LookPath      func(string) (string, error)
}

type rgExitState int

const (
	rgFailed rgExitState = iota
	rgMatched
	rgNoMatch
	rgTimeout
)

type rgRunner func(context.Context, string, []string, func([]byte) bool) (rgExitState, error)

type Service struct {
	options Options
	run     rgRunner
	now     func() time.Time
	gate    chan struct{}
	active  atomic.Int32
}

type normalizedInput struct {
	Keywords          []string
	ShortKeywordCount int
	Limit             int
	StartAt           time.Time
	EndAt             time.Time
	Adjusted          bool
}

type logFile struct {
	path     string
	name     string
	size     int64
	started  time.Time
	modified time.Time
	rank     int
}

type fileListing struct {
	files           []logFile
	truncatedReason string
}

type rgEvent struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		Lines struct {
			Text string `json:"text"`
		} `json:"lines"`
		LineNumber int `json:"line_number"`
	} `json:"data"`
}

func NewService(options Options) *Service {
	return newServiceWithDependencies(options, nil, time.Now)
}

func newServiceWithDependencies(options Options, runner rgRunner, now func() time.Time) *Service {
	if options.MaxFiles <= 0 {
		options.MaxFiles = defaultMaximumFiles
	}
	if options.MaxFiles > maximumFiles {
		options.MaxFiles = maximumFiles
	}
	if options.RetentionDays <= 0 {
		options.RetentionDays = defaultRetentionDays
	}
	if options.RetentionDays > maximumRetentionDays {
		options.RetentionDays = maximumRetentionDays
	}
	if options.LookPath == nil {
		options.LookPath = exec.LookPath
	}
	if runner == nil {
		runner = runRGCommand
	}
	if now == nil {
		now = time.Now
	}
	return &Service{options: options, run: runner, now: now, gate: make(chan struct{}, maxConcurrentSearches)}
}

func (s *Service) Grep(ctx context.Context, input Input) Result {
	startedAt := time.Now()
	normalized := normalizeInput(input, nil, s.now().UTC())
	base := func() Result {
		return Result{
			ElapsedMS:        time.Since(startedAt).Milliseconds(),
			Keywords:         normalized.Keywords,
			StartAt:          formatTime(normalized.StartAt),
			EndAt:            formatTime(normalized.EndAt),
			DefaultRangeDays: defaultRangeDays,
			MaxRangeDays:     maxRangeDays,
			Items:            []Item{},
			Limit:            normalized.Limit,
		}
	}
	if len(normalized.Keywords) == 0 {
		result := base()
		result.Available = true
		if normalized.ShortKeywordCount > 0 {
			result.Message = fmt.Sprintf("grep 关键字至少需要 %d 个字符，请输入更具体的关键字。", minKeywordLength)
		} else {
			result.Message = "请输入要搜索的关键字"
		}
		return result
	}
	if !s.options.FileEnabled {
		return unavailable(base(), "文件日志未启用，无法使用 grep 模式。")
	}
	select {
	case s.gate <- struct{}{}:
		s.active.Add(1)
		defer func() {
			<-s.gate
			s.active.Add(-1)
		}()
	default:
		return unavailable(base(), "已有 grep 搜索正在运行，请稍后重试。")
	}

	listing, err := s.listLogFiles(ctx)
	if err != nil {
		return unavailable(base(), "日志目录暂不可读取，grep 模式不可用。")
	}
	normalized = normalizeInput(input, listing.files, s.now().UTC())
	result := base()
	result.Keywords = normalized.Keywords
	result.StartAt = formatTime(normalized.StartAt)
	result.EndAt = formatTime(normalized.EndAt)
	result.Limit = normalized.Limit
	if len(listing.files) == 0 {
		result.Available = true
		result.Message = joinMessages(listing.warning(), "没有可搜索的日志文件")
		return result
	}
	files := filterFiles(listing.files, normalized)
	if len(files) == 0 {
		result.Available = true
		message := "当前文件时间范围内没有可搜索的日志文件"
		if normalized.Adjusted {
			message = fmt.Sprintf("grep 文件时间范围已自动调整，单次最多 %d 天；当前时间范围内没有可搜索的日志文件。", maxRangeDays)
		}
		result.Message = joinMessages(listing.warning(), message)
		return result
	}
	executable, ok := s.resolveRG()
	if !ok {
		return unavailable(result, "当前运行环境未找到 rg，grep 模式不可用。请配置 JUHE_AI_RG_PATH 或安装 rg。")
	}

	items := make([]Item, 0, normalized.Limit)
	parsedMatchCount := 0
	matchedCount := 0
	noMatchBatches := 0
	parseCapped := false
	timedOut := false
	primaryKeyword := selectPrimaryKeyword(normalized.Keywords)
	normalizedKeywords := make([]string, len(normalized.Keywords))
	for index, keyword := range normalized.Keywords {
		normalizedKeywords[index] = strings.ToLower(keyword)
	}
	filesByPath := make(map[string]logFile, len(files)*2)
	for _, file := range files {
		filesByPath[filepath.Clean(file.path)] = file
		filesByPath[strings.ToLower(filepath.Clean(file.path))] = file
	}
	batches := batchFiles(files, primaryKeyword)
	searchContext, cancelSearch := context.WithTimeout(ctx, maxRGSearchDuration)
	defer cancelSearch()
	for _, batch := range batches {
		args := append(baseRGArgs(primaryKeyword), filePaths(batch)...)
		state, runErr := s.run(searchContext, executable, args, func(raw []byte) bool {
			var event rgEvent
			if len(raw) > maxRGJSONLineLength || json.Unmarshal(raw, &event) != nil || event.Type != "match" {
				return true
			}
			parsedMatchCount++
			stopAfterEvent := parsedMatchCount >= maxParsedMatches
			filePath := filepath.Clean(event.Data.Path.Text)
			file, found := filesByPath[filePath]
			if !found {
				file, found = filesByPath[strings.ToLower(filePath)]
			}
			line := strings.TrimSuffix(strings.TrimSuffix(event.Data.Lines.Text, "\n"), "\r")
			if found && len(line) <= maxLineLength && lineMatchesKeywords(line, normalizedKeywords) && !isRuntimeLogSearchRequestLine(line) {
				matchedCount++
				items = insertLatest(items, buildItem(file, line, event.Data.LineNumber, matchedCount), normalized.Limit)
			}
			if stopAfterEvent {
				parseCapped = true
				return false
			}
			return true
		})
		if state == rgTimeout || errors.Is(searchContext.Err(), context.DeadlineExceeded) {
			timedOut = true
			break
		}
		if runErr != nil || state == rgFailed {
			return unavailable(result, "rg 执行失败，grep 模式暂不可用。")
		}
		if state == rgNoMatch {
			noMatchBatches++
		}
		if parseCapped {
			break
		}
	}

	sort.Slice(items, func(left, right int) bool { return compareItems(items[left], items[right]) < 0 })
	for index := range items {
		items[index].sortTime = time.Time{}
		items[index].fileRank = 0
	}
	result.Available = true
	result.Items = items
	result.Truncated = matchedCount > normalized.Limit || parseCapped || timedOut
	result.ScannedFileCount = len(files)
	result.ElapsedMS = time.Since(startedAt).Milliseconds()
	warnings := []string{listing.warning()}
	if normalized.Adjusted {
		warnings = append(warnings, fmt.Sprintf("grep 文件时间范围已自动调整，单次最多 %d 天", maxRangeDays))
	}
	if normalized.ShortKeywordCount > 0 {
		warnings = append(warnings, fmt.Sprintf("已忽略少于 %d 个字符的短关键字", minKeywordLength))
	}
	if result.Truncated {
		warnings = append(warnings, fmt.Sprintf("结果超过 %d 行，已按最新优先截断显示", normalized.Limit))
	}
	if parseCapped {
		warnings = append(warnings, fmt.Sprintf("grep 命中行超过安全解析上限 %d，已提前停止以保护服务", maxParsedMatches))
	}
	if timedOut {
		warnings = append(warnings, fmt.Sprintf("grep 搜索超过 %d 秒，已提前停止以保护服务", int(maxRGSearchDuration/time.Second)))
	}
	if !parseCapped && !timedOut && noMatchBatches == len(batches) {
		warnings = append(warnings, "没有匹配的日志行")
	}
	result.Message = joinMessages(warnings...)
	return result
}

func (s *Service) Runtime(ctx context.Context) (Runtime, error) {
	now := s.now().UTC()
	listing := fileListing{}
	if s.options.FileEnabled {
		var err error
		listing, err = s.listLogFiles(ctx)
		if err != nil {
			return Runtime{}, err
		}
	}
	normalized := normalizeInput(Input{}, listing.files, now)
	runtime := Runtime{
		DefaultStartAt:        formatTime(normalized.StartAt),
		DefaultEndAt:          formatTime(normalized.EndAt),
		DefaultRangeDays:      defaultRangeDays,
		MaxRangeDays:          maxRangeDays,
		FileRetentionDays:     s.options.RetentionDays,
		ActiveSearchCount:     int(s.active.Load()),
		MaxConcurrentSearches: maxConcurrentSearches,
	}
	if len(listing.files) > 0 {
		earliest := listing.files[0].modified
		for _, file := range listing.files[1:] {
			if file.modified.Before(earliest) {
				earliest = file.modified
			}
		}
		runtime.EarliestFileTime = formatTime(earliest)
	}
	return runtime, nil
}

func normalizeInput(input Input, files []logFile, now time.Time) normalizedInput {
	seen := map[string]struct{}{}
	keywords := make([]string, 0, maxKeywords)
	shortKeywordCount := 0
	for _, raw := range input.Keywords {
		parts := strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == ';' || r == '，' || r == '；' || isWhitespace(r)
		})
		for _, part := range parts {
			keyword := strings.TrimSpace(part)
			if keyword == "" {
				continue
			}
			keyword = truncateRunes(keyword, maxKeywordLength)
			if utf8.RuneCountInString(keyword) < minKeywordLength {
				shortKeywordCount++
				continue
			}
			key := strings.ToLower(keyword)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			keywords = append(keywords, keyword)
			if len(keywords) == maxKeywords {
				break
			}
		}
		if len(keywords) == maxKeywords {
			break
		}
	}
	limit := input.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	endAt := input.EndAt
	adjusted := false
	if endAt.IsZero() {
		endAt = now
	}
	if endAt.After(now) {
		endAt = now
		adjusted = true
	}
	if latest, ok := latestFileTime(files); ok && input.EndAt.IsZero() && endAt.Before(latest) {
		endAt = latest
		adjusted = true
	}
	if earliest, ok := earliestFileTime(files); ok && endAt.Before(earliest) {
		endAt = earliest
		adjusted = true
	}
	startAt := input.StartAt
	if startAt.IsZero() {
		startAt = endAt.Add(-defaultRangeDays * 24 * time.Hour)
	}
	if earliest, ok := earliestFileTime(files); ok && startAt.Before(earliest) {
		startAt = earliest
		adjusted = true
	}
	if startAt.After(endAt) {
		startAt = endAt.Add(-defaultRangeDays * 24 * time.Hour)
		if earliest, ok := earliestFileTime(files); ok && startAt.Before(earliest) {
			startAt = earliest
		}
		adjusted = true
	}
	if endAt.Sub(startAt) > maxRangeDays*24*time.Hour {
		startAt = endAt.Add(-maxRangeDays * 24 * time.Hour)
		adjusted = true
	}
	return normalizedInput{Keywords: keywords, ShortKeywordCount: shortKeywordCount, Limit: limit, StartAt: startAt.UTC(), EndAt: endAt.UTC(), Adjusted: adjusted}
}

func (s *Service) listLogFiles(ctx context.Context) (fileListing, error) {
	directory, err := os.Open(s.options.Directory)
	if err != nil {
		return fileListing{}, err
	}
	defer directory.Close()
	deadline := time.Now().Add(maxDirectoryScanDuration)
	listing := fileListing{}
	entryCount := 0
	for {
		if err := ctx.Err(); err != nil {
			return fileListing{}, err
		}
		if time.Now().After(deadline) {
			listing.truncatedReason = "deadline"
			break
		}
		remaining := maxDirectoryEntries - entryCount
		if remaining <= 0 {
			listing.truncatedReason = "entry_limit"
			break
		}
		batchSize := 100
		if remaining < batchSize {
			batchSize = remaining
		}
		entries, readErr := directory.ReadDir(batchSize)
		for _, entry := range entries {
			entryCount++
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
				continue
			}
			path := filepath.Join(s.options.Directory, entry.Name())
			info, statErr := entry.Info()
			if statErr != nil {
				if errors.Is(statErr, os.ErrNotExist) {
					continue
				}
				return fileListing{}, statErr
			}
			if !info.Mode().IsRegular() {
				continue
			}
			modified := info.ModTime().UTC()
			listing.files = retainNewest(listing.files, logFile{
				path:     path,
				name:     entry.Name(),
				size:     info.Size(),
				started:  fileStartTime(path, info, modified),
				modified: modified,
			}, s.options.MaxFiles)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return fileListing{}, readErr
		}
	}
	for index := range listing.files {
		listing.files[index].rank = index
	}
	return listing, nil
}

func (listing fileListing) warning() string {
	switch listing.truncatedReason {
	case "deadline":
		return "日志目录扫描超过 2 秒，已只使用扫描到的最新日志文件"
	case "entry_limit":
		return fmt.Sprintf("日志目录条目超过 %d 个，已只使用扫描到的最新日志文件", maxDirectoryEntries)
	default:
		return ""
	}
}

func retainNewest(files []logFile, candidate logFile, limit int) []logFile {
	files = append(files, candidate)
	sort.Slice(files, func(left, right int) bool {
		if !files[left].modified.Equal(files[right].modified) {
			return files[left].modified.After(files[right].modified)
		}
		return files[left].name < files[right].name
	})
	if len(files) > limit {
		files = files[:limit]
	}
	return files
}

func (s *Service) resolveRG() (string, bool) {
	if value := strings.TrimSpace(s.options.RGPath); value != "" {
		return value, true
	}
	value, err := s.options.LookPath("rg")
	return value, err == nil && strings.TrimSpace(value) != ""
}

func runRGCommand(ctx context.Context, executable string, args []string, onLine func([]byte) bool) (rgExitState, error) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return rgTimeout, nil
	}
	searchContext, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(searchContext, executable, args...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return rgFailed, errors.New("rg stdout unavailable")
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		if errors.Is(searchContext.Err(), context.DeadlineExceeded) {
			return rgTimeout, nil
		}
		return rgFailed, errors.New("rg start failed")
	}
	reader := bufio.NewReaderSize(stdout, maxRGJSONLineLength)
	matched := false
	stopped := false
	for {
		line, oversized, readErr := readBoundedLine(reader, maxRGJSONLineLength)
		if !oversized && len(line) > 0 {
			var event rgEvent
			if json.Unmarshal(line, &event) == nil && event.Type == "match" {
				matched = true
			}
			if !onLine(line) {
				stopped = true
				cancel()
				break
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			cancel()
			_ = command.Wait()
			return rgFailed, errors.New("rg output failed")
		}
	}
	waitErr := command.Wait()
	if stopped {
		return rgMatched, nil
	}
	if errors.Is(searchContext.Err(), context.DeadlineExceeded) {
		return rgTimeout, nil
	}
	if errors.Is(searchContext.Err(), context.Canceled) {
		return rgFailed, context.Canceled
	}
	if waitErr == nil {
		if matched {
			return rgMatched, nil
		}
		return rgNoMatch, nil
	}
	var exitError *exec.ExitError
	if errors.As(waitErr, &exitError) && exitError.ExitCode() == 1 && !matched {
		return rgNoMatch, nil
	}
	return rgFailed, errors.New("rg execution failed")
}

func readBoundedLine(reader *bufio.Reader, limit int) ([]byte, bool, error) {
	var line []byte
	oversized := false
	for {
		fragment, prefix, err := reader.ReadLine()
		if !oversized {
			if len(line)+len(fragment) > limit {
				line = nil
				oversized = true
			} else {
				line = append(line, fragment...)
			}
		}
		if !prefix {
			return line, oversized, err
		}
		if err != nil {
			return line, oversized, err
		}
	}
}

func filterFiles(files []logFile, input normalizedInput) []logFile {
	result := make([]logFile, 0, len(files))
	for _, file := range files {
		if file.size <= 0 || file.modified.Before(input.StartAt) || file.started.After(input.EndAt) {
			continue
		}
		result = append(result, file)
	}
	return result
}

func batchFiles(files []logFile, pattern string) [][]logFile {
	batches := [][]logFile{}
	batch := []logFile{}
	characters := argumentCharacters(baseRGArgs(pattern))
	for _, file := range files {
		next := len(file.path) + 3
		if len(batch) > 0 && characters+next > maxRGCommandCharacters {
			batches = append(batches, batch)
			batch = nil
			characters = argumentCharacters(baseRGArgs(pattern))
		}
		batch = append(batch, file)
		characters += next
	}
	if len(batch) > 0 {
		batches = append(batches, batch)
	}
	return batches
}

func baseRGArgs(pattern string) []string {
	return []string{"--json", "--fixed-strings", "--ignore-case", "--no-heading", "--color=never", "--max-columns", fmt.Sprint(maxLineLength), "--", pattern}
}

func argumentCharacters(values []string) int {
	total := 0
	for _, value := range values {
		total += len(value) + 3
	}
	return total
}

func filePaths(files []logFile) []string {
	paths := make([]string, len(files))
	for index, file := range files {
		paths[index] = file.path
	}
	return paths
}

func selectPrimaryKeyword(keywords []string) string {
	selected := ""
	for _, keyword := range keywords {
		if len(keyword) > len(selected) {
			selected = keyword
		}
	}
	return selected
}

func lineMatchesKeywords(line string, keywords []string) bool {
	lower := strings.ToLower(line)
	for _, keyword := range keywords {
		if !strings.Contains(lower, keyword) {
			return false
		}
	}
	return true
}

func isRuntimeLogSearchRequestLine(line string) bool {
	var record map[string]any
	if json.Unmarshal([]byte(line), &record) != nil {
		return false
	}
	event := stringField(record["event"])
	if event != "http_request_completed" && event != "http_request_closed" {
		return false
	}
	return isRuntimeLogSearchPath(stringField(record["originalUrl"])) || isRuntimeLogSearchPath(stringField(record["path"]))
}

func isRuntimeLogSearchPath(value string) bool {
	path := strings.TrimRight(strings.SplitN(value, "?", 2)[0], "/")
	return path == "/__aisys__/api/runtime-logs" || strings.HasPrefix(path, "/__aisys__/api/runtime-logs/")
}

func buildItem(file logFile, line string, lineNumber, sequence int) Item {
	item := Item{
		ID:         fmt.Sprintf("%s:%d:%d", file.name, lineNumber, sequence),
		File:       file.name,
		FileName:   file.name,
		LineNumber: lineNumber,
		Time:       "",
		Level:      "info",
		Line:       line,
		sortTime:   file.modified,
		fileRank:   file.rank,
	}
	var record map[string]any
	if json.Unmarshal([]byte(line), &record) != nil {
		return item
	}
	item.Time, item.sortTime = runtimeLogTime(record["time"], file.modified)
	item.Level = normalizeLevel(record["level"])
	item.TraceID = stringField(record["traceId"])
	item.Event = stringField(record["event"])
	item.Message = firstNonEmpty(stringField(record["msg"]), stringField(record["message"]))
	item.ErrorMessage = firstNonEmpty(stringField(record["errorMessage"]), errorMessage(record["err"]))
	return item
}

func insertLatest(items []Item, item Item, limit int) []Item {
	items = append(items, item)
	sort.Slice(items, func(left, right int) bool { return compareItems(items[left], items[right]) < 0 })
	if len(items) > limit {
		items = items[:limit]
	}
	return items
}

func compareItems(left, right Item) int {
	if !left.sortTime.Equal(right.sortTime) {
		if left.sortTime.After(right.sortTime) {
			return -1
		}
		return 1
	}
	if left.fileRank != right.fileRank {
		return left.fileRank - right.fileRank
	}
	return right.LineNumber - left.LineNumber
}

func runtimeLogTime(value any, fallback time.Time) (string, time.Time) {
	switch typed := value.(type) {
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return "", fallback
		}
		if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return text, parsed
		}
		return text, fallback
	case float64:
		parsed := time.UnixMilli(int64(typed)).UTC()
		return formatTime(parsed), parsed
	default:
		return "", fallback
	}
}

func normalizeLevel(value any) string {
	if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
		return strings.ToLower(strings.TrimSpace(text))
	}
	number, ok := value.(float64)
	if !ok {
		return "info"
	}
	switch {
	case number >= 60:
		return "fatal"
	case number >= 50:
		return "error"
	case number >= 40:
		return "warn"
	case number >= 30:
		return "info"
	case number >= 20:
		return "debug"
	default:
		return "trace"
	}
}

func stringField(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func errorMessage(value any) string {
	record, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	return stringField(record["message"])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func unavailable(result Result, message string) Result {
	result.Available = false
	result.Items = []Item{}
	result.Truncated = false
	result.ScannedFileCount = 0
	result.Message = message
	return result
}

func joinMessages(messages ...string) string {
	filtered := messages[:0]
	for _, message := range messages {
		if strings.TrimSpace(message) != "" {
			filtered = append(filtered, message)
		}
	}
	return strings.Join(filtered, "；")
}

func earliestFileTime(files []logFile) (time.Time, bool) {
	if len(files) == 0 {
		return time.Time{}, false
	}
	value := effectiveFileStart(files[0])
	for _, file := range files[1:] {
		started := effectiveFileStart(file)
		if started.Before(value) {
			value = started
		}
	}
	return value, true
}

func effectiveFileStart(file logFile) time.Time {
	if file.started.IsZero() {
		return file.modified
	}
	return file.started
}

func latestFileTime(files []logFile) (time.Time, bool) {
	if len(files) == 0 {
		return time.Time{}, false
	}
	value := files[0].modified
	for _, file := range files[1:] {
		if file.modified.After(value) {
			value = file.modified
		}
	}
	return value, true
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func isWhitespace(value rune) bool {
	return strings.ContainsRune("\t\v\f \u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u202f\u205f\u3000\ufeff\n\r\u2028\u2029", value)
}

func formatTime(value time.Time) string {
	return value.UTC().Format(runtimeLogTimestampLayout)
}
