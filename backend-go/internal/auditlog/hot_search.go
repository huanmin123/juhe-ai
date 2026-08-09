package auditlog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	hotSearchFilePrefix      = "audit-hot-"
	hotSearchFileSuffix      = ".ndjson"
	hotSearchBucket          = time.Hour
	defaultHotSearchLimit    = 50
	maxHotSearchLimit        = 500
	defaultHotSearchFiles    = 256
	defaultHotSearchLines    = 100000
	maxHotSearchScanEntries  = 4096
	maxHotSearchLineBytes    = 256 * 1024
	maxHotSearchChunkBytes   = 64 * 1024
	maxHotSearchKeywords     = 32
	maxHotSearchKeywordRunes = 100
)

type hotSearchLine struct {
	AuditLogID    string `json:"auditLogId"`
	CreatedAt     string `json:"createdAt"`
	TraceID       string `json:"traceId,omitempty"`
	TrafficSource string `json:"trafficSource,omitempty"`
	Sequence      int    `json:"sequence"`
	Text          string `json:"text"`
}

// AppendHotSearch appends bounded NDJSON search records while holding the same
// owner fence used by audit persistence. A stale owner cannot publish lines.
func (s *sqlStore) AppendHotSearch(ctx context.Context, lease OwnerLease, inputs []AuditLogInput) (int, error) {
	if len(inputs) == 0 {
		return 0, nil
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return 0, err
	}
	if s.mode == ModeSQLite {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("开始 F3 hot-search 追加事务失败: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := s.verifyLeaseTx(ctx, tx, lease); err != nil {
		return 0, err
	}
	lines, err := buildHotSearchLines(s.hotDir, inputs)
	if err != nil {
		return 0, err
	}
	if len(lines) == 0 {
		return 0, nil
	}
	s.hotMu.Lock()
	defer s.hotMu.Unlock()
	appended := 0
	for path, contents := range lines {
		if err := appendHotSearchFile(path, contents); err != nil {
			return 0, err
		}
		appended += len(contents)
	}
	if err := s.verifyLeaseBeforeCommit(ctx, tx, lease); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("提交 F3 hot-search 追加事务失败: %w", err)
	}
	committed = true
	return appended, nil
}

// CleanupHotSearch removes complete hourly buckets before cutoff under the
// same owner fence as retention. A bucket is removed only when its end is not
// newer than the cutoff, so a partial current hour is never truncated.
func (s *sqlStore) CleanupHotSearch(ctx context.Context, lease OwnerLease, cutoff time.Time, maxFiles int) (int64, error) {
	if cutoff.IsZero() {
		return 0, fmt.Errorf("F3 hot-search cutoff 不能为空")
	}
	if maxFiles <= 0 {
		maxFiles = defaultHotSearchFiles
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return 0, err
	}
	if s.mode == ModeSQLite {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("开始 F3 hot-search 清理事务失败: %w", err)
	}
	defer tx.Rollback()
	if err := s.verifyLeaseTx(ctx, tx, lease); err != nil {
		return 0, err
	}
	s.hotMu.Lock()
	deleted, err := s.cleanupHotSearchFilesBefore(cutoff.UTC(), maxFiles)
	s.hotMu.Unlock()
	if err != nil {
		return deleted, err
	}
	if err := s.verifyLeaseBeforeCommit(ctx, tx, lease); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("提交 F3 hot-search 清理事务失败: %w", err)
	}
	return deleted, nil
}

func appendHotSearchFile(path string, lines []hotSearchLine) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("创建 F3 hot-search 目录失败: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return fmt.Errorf("打开 F3 hot-search 文件失败: %w", err)
	}
	encoder := json.NewEncoder(file)
	for _, line := range lines {
		if err := encoder.Encode(line); err != nil {
			_ = file.Close()
			return fmt.Errorf("写入 F3 hot-search 文件失败: %w", err)
		}
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("同步 F3 hot-search 文件失败: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("关闭 F3 hot-search 文件失败: %w", err)
	}
	return nil
}

func buildHotSearchLines(root string, inputs []AuditLogInput) (map[string][]hotSearchLine, error) {
	result := make(map[string][]hotSearchLine)
	for _, input := range inputs {
		if strings.TrimSpace(input.ID) == "" {
			continue
		}
		if isNonPersistedTrafficSource(string(input.TrafficSource)) {
			continue
		}
		createdAt := strings.TrimSpace(input.CreatedAt)
		if createdAt == "" {
			createdAt = strings.TrimSpace(input.EndedAt)
		}
		when, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("F3 hot-search createdAt 非法: auditLogID=%q: %w", input.ID, err)
		}
		when = when.UTC()
		text := buildHotSearchText(input)
		if text == "" {
			continue
		}
		for sequence, chunk := range chunkHotSearchText(text) {
			line := hotSearchLine{AuditLogID: input.ID, CreatedAt: when.Format(time.RFC3339Nano), TraceID: input.TraceID, TrafficSource: string(input.TrafficSource), Sequence: sequence, Text: chunk}
			path := filepath.Join(root, hotSearchFileName(when))
			result[path] = append(result[path], line)
		}
	}
	return result, nil
}

func isNonPersistedTrafficSource(value string) bool {
	for _, candidate := range nonPersistedTrafficSources {
		if value == candidate {
			return true
		}
	}
	return false
}

func buildHotSearchText(input AuditLogInput) string {
	includePayloadBody := !input.Success || input.AuditOutcome != AuditOutcomeSuccess || strings.HasPrefix(input.SampleReason, "success_sample_")
	parts := []string{input.TraceID, string(input.TrafficSource), input.Method, input.Path, input.QueryString, input.Model, input.UpstreamModel, input.PricingModel, input.ModelMappingSource, input.ClientIP, input.UserAgent, string(input.AuditOutcome), input.ErrorPhase, input.ErrorCode, input.ErrorMessage, input.SystemAccountID, input.APIKeyID, input.GroupID, input.AccountID, input.ProviderCode}
	if input.ModelMappingApplied != nil && *input.ModelMappingApplied {
		parts = append(parts, "model_mapping_applied")
	}
	if input.FinalStatusCode != nil {
		parts = append(parts, strconv.Itoa(*input.FinalStatusCode))
	}
	for _, attempt := range input.Attempts {
		parts = append(parts, strconv.Itoa(attempt.AttemptIndex), attempt.AccountID, attempt.GroupID, attempt.ProviderCode, attempt.UpstreamMethod, attempt.UpstreamURL, attempt.ErrorPhase, attempt.ErrorCode, attempt.ErrorMessage)
		if attempt.UpstreamStatusCode != nil {
			parts = append(parts, strconv.Itoa(*attempt.UpstreamStatusCode))
		}
	}
	for _, payload := range input.Payloads {
		parts = append(parts, string(payload.PartType), payload.ContentType, payload.ContentEncoding, payload.BodySHA256, string(payload.CaptureStatus), string(payload.DropReason))
		if payload.RawBodySizeBytes != nil {
			parts = append(parts, strconv.FormatInt(*payload.RawBodySizeBytes, 10))
		}
		if payload.Headers != nil {
			if encoded, err := json.Marshal(payload.Headers); err == nil {
				parts = append(parts, string(encoded))
			}
		}
		if includePayloadBody && payload.Body.Present {
			parts = append(parts, string(payload.Body.Bytes))
		}
	}
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			filtered = append(filtered, part)
		}
	}
	return strings.Join(filtered, " ")
}

func chunkHotSearchText(text string) []string {
	if len(text) <= maxHotSearchChunkBytes {
		return []string{text}
	}
	chunks := make([]string, 0, (len(text)+maxHotSearchChunkBytes-1)/maxHotSearchChunkBytes)
	for len(text) > 0 {
		n := maxHotSearchChunkBytes
		if len(text) < n {
			n = len(text)
		}
		chunks = append(chunks, text[:n])
		text = text[n:]
	}
	return chunks
}

func hotSearchFileName(when time.Time) string {
	return hotSearchFilePrefix + when.UTC().Format("2006010215") + hotSearchFileSuffix
}

// SearchHotSearch scans only bounded hourly files and returns newest unique IDs.
func (s *sqlStore) SearchHotSearch(ctx context.Context, options HotSearchOptions) (HotSearchResult, error) {
	select {
	case <-ctx.Done():
		return HotSearchResult{}, ctx.Err()
	default:
	}
	if len(options.Keywords) == 0 {
		return HotSearchResult{}, fmt.Errorf("hot-search 至少需要一个关键词")
	}
	keywords := normalizeHotKeywords(options.Keywords)
	if len(keywords) == 0 {
		return HotSearchResult{}, fmt.Errorf("hot-search 关键词不能为空且至少两个字符")
	}
	now := time.Now().UTC()
	end := options.EndAt
	if end.IsZero() || end.After(now) {
		end = now
	}
	start := options.StartAt
	if start.IsZero() {
		start = end.Add(-24 * time.Hour)
	}
	if start.After(end) {
		return HotSearchResult{}, fmt.Errorf("hot-search startAt 不得晚于 endAt")
	}
	limit := options.Limit
	if limit <= 0 {
		limit = defaultHotSearchLimit
	}
	if limit > maxHotSearchLimit {
		limit = maxHotSearchLimit
	}
	maxFiles := options.MaxFiles
	if maxFiles <= 0 {
		maxFiles = defaultHotSearchFiles
	}
	maxLines := options.MaxLines
	if maxLines <= 0 {
		maxLines = defaultHotSearchLines
	}
	files, fileTruncated, err := s.listHotSearchFiles(start.UTC(), end.UTC(), maxFiles)
	if err != nil {
		return HotSearchResult{}, err
	}
	seen := make(map[string]time.Time)
	linesRead := 0
	truncated := fileTruncated
	for _, filePath := range files {
		read, err := scanHotSearchFile(ctx, filePath, keywords, start.UTC(), end.UTC(), maxLines-linesRead, seen)
		if err != nil {
			return HotSearchResult{}, err
		}
		linesRead += read
		if linesRead >= maxLines {
			truncated = true
			break
		}
	}
	type match struct {
		id   string
		when time.Time
	}
	matches := make([]match, 0, len(seen))
	for id, when := range seen {
		matches = append(matches, match{id: id, when: when})
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].when.Equal(matches[j].when) {
			return matches[i].id < matches[j].id
		}
		return matches[i].when.After(matches[j].when)
	})
	if len(matches) > limit {
		matches = matches[:limit]
		truncated = true
	}
	ids := make([]string, 0, len(matches))
	for _, item := range matches {
		ids = append(ids, item.id)
	}
	return HotSearchResult{AuditLogIDs: ids, ScannedFiles: len(files), Truncated: truncated}, nil
}

func normalizeHotKeywords(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		runes := []rune(value)
		if len(runes) > maxHotSearchKeywordRunes {
			value = string(runes[:maxHotSearchKeywordRunes])
		}
		if len([]rune(value)) < 2 {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
		if len(result) >= maxHotSearchKeywords {
			break
		}
	}
	return result
}

func (s *sqlStore) listHotSearchFiles(start, end time.Time, maxFiles int) ([]string, bool, error) {
	entries, err := os.ReadDir(s.hotDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("读取 F3 hot-search 目录失败: %w", err)
	}
	type candidate struct {
		path   string
		bucket time.Time
	}
	candidates := make([]candidate, 0)
	entryScanTruncated := len(entries) > maxHotSearchScanEntries
	if entryScanTruncated {
		entries = entries[:maxHotSearchScanEntries]
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		bucket, ok := parseHotSearchBucket(entry.Name())
		if !ok {
			continue
		}
		if bucket.Add(hotSearchBucket).Before(start) || bucket.After(end) {
			continue
		}
		candidates = append(candidates, candidate{path: filepath.Join(s.hotDir, entry.Name()), bucket: bucket})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].bucket.After(candidates[j].bucket) })
	truncated := entryScanTruncated || len(candidates) > maxFiles
	if truncated {
		candidates = candidates[:maxFiles]
	}
	paths := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		paths = append(paths, candidate.path)
	}
	return paths, truncated, nil
}

func parseHotSearchBucket(name string) (time.Time, bool) {
	if !strings.HasPrefix(name, hotSearchFilePrefix) || !strings.HasSuffix(name, hotSearchFileSuffix) {
		return time.Time{}, false
	}
	value := strings.TrimSuffix(strings.TrimPrefix(name, hotSearchFilePrefix), hotSearchFileSuffix)
	if len(value) != 10 {
		return time.Time{}, false
	}
	when, err := time.Parse("2006010215", value)
	return when.UTC(), err == nil
}

func scanHotSearchFile(ctx context.Context, path string, keywords []string, start, end time.Time, budget int, seen map[string]time.Time) (int, error) {
	if budget <= 0 {
		return 0, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("打开 F3 hot-search 文件失败: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxHotSearchLineBytes)
	lines := 0
	for lines < budget && scanner.Scan() {
		lines++
		select {
		case <-ctx.Done():
			return lines, ctx.Err()
		default:
		}
		var line hotSearchLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		when, err := time.Parse(time.RFC3339Nano, line.CreatedAt)
		if err != nil || when.Before(start) || when.After(end) || strings.TrimSpace(line.AuditLogID) == "" {
			continue
		}
		text := strings.ToLower(line.Text)
		matched := false
		for _, keyword := range keywords {
			if strings.Contains(text, keyword) {
				matched = true
				break
			}
		}
		if matched {
			if previous, ok := seen[line.AuditLogID]; !ok || when.After(previous) {
				seen[line.AuditLogID] = when
			}
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return lines, fmt.Errorf("读取 F3 hot-search 文件失败: %w", err)
	}
	return lines, nil
}

func (s *sqlStore) cleanupHotSearchFilesBefore(cutoff time.Time, maxFiles int) (int64, error) {
	entries, err := os.ReadDir(s.hotDir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("读取 F3 hot-search 清理目录失败: %w", err)
	}
	var deleted int64
	if len(entries) > maxHotSearchScanEntries {
		entries = entries[:maxHotSearchScanEntries]
	}
	for _, entry := range entries {
		if deleted >= int64(maxFiles) || !entry.Type().IsRegular() {
			continue
		}
		bucket, ok := parseHotSearchBucket(entry.Name())
		if !ok || bucket.Add(hotSearchBucket).After(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(s.hotDir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return deleted, fmt.Errorf("删除 F3 hot-search 文件失败: %w", err)
		}
		deleted++
	}
	return deleted, nil
}
