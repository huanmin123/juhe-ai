package statsverify

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// clientIPAggregate mirrors the ClientIpAggregate accumulator of
// storage/client-ip-stats-writer.ts.
type clientIPAggregate struct {
	normalized *NormalizedClientIP
	statDate   string
	accountID  string

	requestCount      int
	successCount      int
	errorCount        int
	inputTokens       int
	outputTokens      int
	cacheReadTokens   int
	cacheReadCostUsd  float64
	cacheWriteTokens  int
	cacheWrite1hTok   int
	cacheWriteCostUsd float64
	thinkingTokens    int
	inputImageTokens  int
	outputImageTokens int
	totalCostUsd      float64
	durationMsSum     int
	durationMsCount   int
	durationMsMax     int
	firstTokenMsSum   int
	firstTokenMsCount int
	firstSeenAt       string
	lastUsedAt        string
	lastErrorAt       string
}

// buildClientIPAggregates mirrors buildClientIpAggregates
// (client-ip-stats-writer.ts lines 91-175): dimension keys are
// `ipHash:statDate` and `ipHash:accountId:statDate`; rows without a
// normalizable client_ip are skipped; rows without account_id only
// contribute to the ip dimension.
func buildClientIPAggregates(rows []UsageStatsRecordRow, location *time.Location) ([]*clientIPAggregate, []*clientIPAggregate, error) {
	ipAggregates := make(map[string]*clientIPAggregate)
	accountAggregates := make(map[string]*clientIPAggregate)
	ipOrder := make([]string, 0)
	accountOrder := make([]string, 0)

	for _, row := range rows {
		rawClientIP := ""
		if row.ClientIP != nil {
			rawClientIP = *row.ClientIP
		}
		normalized := NormalizeClientIPForStats(rawClientIP)
		if normalized == nil {
			continue
		}
		createdAtTime, err := ParseRFC3339(row.CreatedAt, "使用记录 created_at")
		if err != nil {
			return nil, nil, err
		}
		createdAt := NowIso(createdAtTime)
		statDate := DateKeyIn(createdAtTime, location)
		accumulator := AccumulatorFromRecord(row)

		ipKey := normalized.IPHash + ":" + statDate
		if current, ok := ipAggregates[ipKey]; ok {
			addAccumulatorToClientIPAggregate(current, accumulator, createdAt)
		} else {
			created := newClientIPAggregate(normalized, statDate, "", accumulator, createdAt)
			ipAggregates[ipKey] = created
			ipOrder = append(ipOrder, ipKey)
		}

		accountID := ""
		if row.AccountID != nil {
			accountID = trimSpaces(*row.AccountID)
		}
		if accountID == "" {
			continue
		}
		accountKey := normalized.IPHash + ":" + accountID + ":" + statDate
		if current, ok := accountAggregates[accountKey]; ok {
			addAccumulatorToClientIPAggregate(current, accumulator, createdAt)
		} else {
			created := newClientIPAggregate(normalized, statDate, accountID, accumulator, createdAt)
			accountAggregates[accountKey] = created
			accountOrder = append(accountOrder, accountKey)
		}
	}

	ips := make([]*clientIPAggregate, 0, len(ipOrder))
	for _, key := range ipOrder {
		ips = append(ips, ipAggregates[key])
	}
	accounts := make([]*clientIPAggregate, 0, len(accountOrder))
	for _, key := range accountOrder {
		accounts = append(accounts, accountAggregates[key])
	}
	return ips, accounts, nil
}

func newClientIPAggregate(normalized *NormalizedClientIP, statDate, accountID string, acc UsageStatsAccumulator, createdAt string) *clientIPAggregate {
	return &clientIPAggregate{
		normalized:        normalized,
		statDate:          statDate,
		accountID:         accountID,
		requestCount:      acc.RequestCount,
		successCount:      acc.SuccessCount,
		errorCount:        acc.ErrorCount,
		inputTokens:       acc.InputTokens,
		outputTokens:      acc.OutputTokens,
		cacheReadTokens:   acc.CacheReadTokens,
		cacheReadCostUsd:  acc.CacheReadCostUsd,
		cacheWriteTokens:  acc.CacheWriteTokens,
		cacheWrite1hTok:   acc.CacheWrite1hTok,
		cacheWriteCostUsd: acc.CacheWriteCostUsd,
		thinkingTokens:    acc.ThinkingTokens,
		inputImageTokens:  acc.InputImageTokens,
		outputImageTokens: acc.OutputImageTokens,
		totalCostUsd:      acc.TotalCostUsd,
		durationMsSum:     acc.DurationMsSum,
		durationMsCount:   acc.DurationMsCount,
		durationMsMax:     acc.DurationMsMax,
		firstTokenMsSum:   acc.FirstTokenMsSum,
		firstTokenMsCount: acc.FirstTokenMsCount,
		firstSeenAt:       createdAt,
		lastUsedAt:        createdAt,
		lastErrorAt:       acc.LastErrorAt,
	}
}

// addAccumulatorToClientIPAggregate mirrors addAccumulatorToClientIpAggregate
// (client-ip-stats-writer.ts lines 655-694): numeric fields add,
// duration_ms_max takes the max, first/last seen compare RFC3339 instants,
// lastErrorAt keeps the newest error instant.
func addAccumulatorToClientIPAggregate(target *clientIPAggregate, acc UsageStatsAccumulator, createdAt string) {
	target.requestCount += acc.RequestCount
	target.successCount += acc.SuccessCount
	target.errorCount += acc.ErrorCount
	target.inputTokens += acc.InputTokens
	target.outputTokens += acc.OutputTokens
	target.cacheReadTokens += acc.CacheReadTokens
	target.cacheReadCostUsd += acc.CacheReadCostUsd
	target.cacheWriteTokens += acc.CacheWriteTokens
	target.cacheWrite1hTok += acc.CacheWrite1hTok
	target.cacheWriteCostUsd += acc.CacheWriteCostUsd
	target.thinkingTokens += acc.ThinkingTokens
	target.inputImageTokens += acc.InputImageTokens
	target.outputImageTokens += acc.OutputImageTokens
	target.totalCostUsd += acc.TotalCostUsd
	target.durationMsSum += acc.DurationMsSum
	target.durationMsCount += acc.DurationMsCount
	if acc.DurationMsMax > target.durationMsMax {
		target.durationMsMax = acc.DurationMsMax
	}
	target.firstTokenMsSum += acc.FirstTokenMsSum
	target.firstTokenMsCount += acc.FirstTokenMsCount

	createdAtMs, err := ParseRFC3339(createdAt, "客户端 IP 统计 createdAt")
	if err != nil {
		// Callers pre-normalize createdAt; keep Node's fail-fast behaviour.
		panic(err)
	}
	firstSeenAtMs, err := ParseRFC3339(target.firstSeenAt, "客户端 IP 统计 firstSeenAt")
	if err != nil {
		panic(err)
	}
	lastUsedAtMs, err := ParseRFC3339(target.lastUsedAt, "客户端 IP 统计 lastUsedAt")
	if err != nil {
		panic(err)
	}
	if createdAtMs.Before(firstSeenAtMs) {
		target.firstSeenAt = createdAt
	}
	if createdAtMs.After(lastUsedAtMs) {
		target.lastUsedAt = createdAt
	}
	if acc.LastErrorAt != "" {
		lastErrorAtMs, err := ParseRFC3339(acc.LastErrorAt, "客户端 IP 统计 lastErrorAt")
		if err != nil {
			panic(err)
		}
		replace := target.lastErrorAt == ""
		if !replace {
			currentMs, err := ParseRFC3339(target.lastErrorAt, "客户端 IP 统计 lastErrorAt")
			if err != nil {
				panic(err)
			}
			replace = lastErrorAtMs.After(currentMs)
		}
		if replace {
			target.lastErrorAt = acc.LastErrorAt
		}
	}
}

// writeClientIPAggregates mirrors writeClientIpAggregatesAsync
// (client-ip-stats-writer.ts lines 194-213): registry upsert, both daily
// upserts, dirty marking, then current-window stale marking.
func (s *Store) writeClientIPAggregates(ctx context.Context, tx execer, rows []UsageStatsRecordRow, updatedAt string, location *time.Location, now time.Time) error {
	ips, accounts, err := buildClientIPAggregates(rows, location)
	if err != nil {
		return err
	}
	if len(ips) == 0 && len(accounts) == 0 {
		return nil
	}

	dirty := make(map[string]struct{})
	for _, aggregate := range ips {
		dirty[aggregate.normalized.IPHash] = struct{}{}
	}
	for _, aggregate := range accounts {
		dirty[aggregate.normalized.IPHash] = struct{}{}
	}

	if err := s.upsertClientIPRegistry(ctx, tx, ips, updatedAt); err != nil {
		return err
	}
	for _, aggregate := range ips {
		if err := s.upsertClientIPDaily(ctx, tx, s.statsTable("client_ip_stats_daily"), "", aggregate, updatedAt); err != nil {
			return err
		}
	}
	for _, aggregate := range accounts {
		if err := s.upsertClientIPDaily(ctx, tx, s.statsTable("client_ip_account_stats_daily"), "account_id, ", aggregate, updatedAt); err != nil {
			return err
		}
	}

	ipHashes := sortedSetKeys(dirty)
	for _, ipHash := range ipHashes {
		if err := s.markClientIPRangeWindowDirty(ctx, tx, s.statsTable("client_ip_range_window_dirty_ips"), ipHash, updatedAt); err != nil {
			return err
		}
		if err := s.markClientIPRangeWindowDirty(ctx, tx, s.statsTable("client_ip_account_range_window_dirty_ips"), ipHash, updatedAt); err != nil {
			return err
		}
	}
	s.rememberDirtyIPHashes(ipHashes)

	return s.markCurrentClientIPUsageRangeWindowsStale(ctx, tx, updatedAt, location, now)
}

// upsertClientIPRegistry mirrors upsertClientIpRegistryAsync: min
// first_seen_at, max last_seen_at, identity columns overwritten. Node's
// SQLite path uses INSERT OR IGNORE + UPDATE, which converges to the same
// row state.
func (s *Store) upsertClientIPRegistry(ctx context.Context, tx execer, ips []*clientIPAggregate, updatedAt string) error {
	entries := registryAggregatesFromIPAggregates(ips)
	table := s.statsTable("client_ip_registry")
	for _, entry := range entries {
		query := fmt.Sprintf(`
			INSERT INTO %s (ip_hash, bucket_no, aggregate_ip_key, client_ip, ip_version, first_seen_at, last_seen_at, created_at, updated_at)
			VALUES (%s)
			ON CONFLICT(ip_hash) DO UPDATE SET
			  bucket_no = EXCLUDED.bucket_no,
			  aggregate_ip_key = EXCLUDED.aggregate_ip_key,
			  client_ip = EXCLUDED.client_ip,
			  ip_version = EXCLUDED.ip_version,
			  first_seen_at = CASE WHEN %s.first_seen_at > EXCLUDED.first_seen_at THEN EXCLUDED.first_seen_at ELSE %s.first_seen_at END,
			  last_seen_at = CASE WHEN %s.last_seen_at < EXCLUDED.last_seen_at THEN EXCLUDED.last_seen_at ELSE %s.last_seen_at END,
			  updated_at = EXCLUDED.updated_at
		`, table, s.placeholders(9), table, table, table, table)
		if _, err := tx.ExecContext(ctx, query,
			entry.normalized.IPHash, entry.normalized.BucketNo, entry.normalized.AggregateIPKey,
			entry.normalized.ClientIP, entry.normalized.IPVersion,
			entry.firstSeenAt, entry.lastSeenAt, updatedAt, updatedAt); err != nil {
			return fmt.Errorf("upsert client_ip_registry 失败: %w", err)
		}
	}
	return nil
}

type clientIPRegistryEntry struct {
	normalized  *NormalizedClientIP
	firstSeenAt string
	lastSeenAt  string
}

// registryAggregatesFromIPAggregates mirrors registryAggregatesFromIpAggregates:
// one entry per ipHash with the minimum firstSeenAt and maximum lastUsedAt.
func registryAggregatesFromIPAggregates(ips []*clientIPAggregate) []clientIPRegistryEntry {
	entries := make(map[string]*clientIPRegistryEntry)
	order := make([]string, 0)
	for _, aggregate := range ips {
		current, ok := entries[aggregate.normalized.IPHash]
		if !ok {
			entries[aggregate.normalized.IPHash] = &clientIPRegistryEntry{
				normalized:  aggregate.normalized,
				firstSeenAt: aggregate.firstSeenAt,
				lastSeenAt:  aggregate.lastUsedAt,
			}
			order = append(order, aggregate.normalized.IPHash)
			continue
		}
		aggregateFirst, err := ParseRFC3339(aggregate.firstSeenAt, "客户端 IP 注册表时间")
		if err != nil {
			panic(err)
		}
		currentFirst, err := ParseRFC3339(current.firstSeenAt, "客户端 IP 注册表时间")
		if err != nil {
			panic(err)
		}
		aggregateLast, err := ParseRFC3339(aggregate.lastUsedAt, "客户端 IP 注册表时间")
		if err != nil {
			panic(err)
		}
		currentLast, err := ParseRFC3339(current.lastSeenAt, "客户端 IP 注册表时间")
		if err != nil {
			panic(err)
		}
		if aggregateFirst.Before(currentFirst) {
			current.firstSeenAt = aggregate.firstSeenAt
		}
		if aggregateLast.After(currentLast) {
			current.lastSeenAt = aggregate.lastUsedAt
		}
	}
	result := make([]clientIPRegistryEntry, 0, len(order))
	for _, ipHash := range order {
		result = append(result, *entries[ipHash])
	}
	return result
}

// upsertClientIPDaily mirrors upsertClientIpDailyAsync /
// upsertClientIpAccountDailyAsync. The additive UPSERT is the idempotence
// boundary: re-aggregating the same cursor window would double-count, so the
// cursor advance is part of the same transaction.
func (s *Store) upsertClientIPDaily(ctx context.Context, tx execer, table, accountColumns string, aggregate *clientIPAggregate, updatedAt string) error {
	conflictTarget := "ip_hash, stat_date"
	valueAccountID := any(nil)
	ref := table
	if accountColumns != "" {
		conflictTarget = "ip_hash, account_id, stat_date"
		valueAccountID = aggregate.accountID
	}
	lastErrorAt := any(nil)
	if aggregate.lastErrorAt != "" {
		lastErrorAt = aggregate.lastErrorAt
	}
	metricColumns := []string{
		"request_count", "success_count", "error_count",
		"input_tokens", "output_tokens", "cache_read_tokens", "cache_read_cost_usd",
		"cache_write_tokens", "cache_write_1h_tokens", "cache_write_cost_usd", "thinking_tokens",
		"input_image_tokens", "output_image_tokens", "total_cost_usd",
		"duration_ms_sum", "duration_ms_count",
	}
	refUpdates := make([]string, 0, len(metricColumns))
	for _, column := range metricColumns {
		refUpdates = append(refUpdates, fmt.Sprintf("%s = %s.%s + EXCLUDED.%s", column, ref, column, column))
	}
	keyColumns := "ip_hash, stat_date"
	placeholders := s.placeholders(24)
	if accountColumns != "" {
		// Node column order: (ip_hash, account_id, stat_date, ...).
		keyColumns = "ip_hash, account_id, stat_date"
		placeholders = s.placeholders(25)
	}
	query := fmt.Sprintf(`
		INSERT INTO %s (%s, request_count, success_count, error_count,
		  input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
		  cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens,
		  input_image_tokens, output_image_tokens, total_cost_usd,
		  duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count,
		  last_used_at, last_error_at, updated_at)
		VALUES (%s)
		ON CONFLICT(%s) DO UPDATE SET
		  %s,
		  duration_ms_max = %s,
		  first_token_ms_sum = %s.first_token_ms_sum + EXCLUDED.first_token_ms_sum,
		  first_token_ms_count = %s.first_token_ms_count + EXCLUDED.first_token_ms_count,
		  last_used_at = CASE WHEN %s.last_used_at IS NULL OR EXCLUDED.last_used_at > %s.last_used_at THEN EXCLUDED.last_used_at ELSE %s.last_used_at END,
		  last_error_at = CASE WHEN EXCLUDED.last_error_at IS NULL THEN %s.last_error_at WHEN %s.last_error_at IS NULL OR EXCLUDED.last_error_at > %s.last_error_at THEN EXCLUDED.last_error_at ELSE %s.last_error_at END,
		  updated_at = EXCLUDED.updated_at
	`,
		table,
		keyColumns,
		placeholders,
		conflictTarget,
		strings.Join(refUpdates, ",\n\t\t  "),
		s.greatest(ref+".duration_ms_max", "EXCLUDED.duration_ms_max"),
		ref, ref,
		ref, ref, ref,
		ref, ref, ref, ref,
	)
	var err error
	if accountColumns != "" {
		// Node column order: (ip_hash, account_id, stat_date, ...).
		_, err = tx.ExecContext(ctx, query,
			aggregate.normalized.IPHash, valueAccountID, aggregate.statDate,
			aggregate.requestCount, aggregate.successCount, aggregate.errorCount,
			aggregate.inputTokens, aggregate.outputTokens, aggregate.cacheReadTokens, aggregate.cacheReadCostUsd,
			aggregate.cacheWriteTokens, aggregate.cacheWrite1hTok, aggregate.cacheWriteCostUsd, aggregate.thinkingTokens,
			aggregate.inputImageTokens, aggregate.outputImageTokens, aggregate.totalCostUsd,
			aggregate.durationMsSum, aggregate.durationMsCount, aggregate.durationMsMax, aggregate.firstTokenMsSum, aggregate.firstTokenMsCount,
			aggregate.lastUsedAt, lastErrorAt, updatedAt)
	} else {
		_, err = tx.ExecContext(ctx, query,
			aggregate.normalized.IPHash, aggregate.statDate,
			aggregate.requestCount, aggregate.successCount, aggregate.errorCount,
			aggregate.inputTokens, aggregate.outputTokens, aggregate.cacheReadTokens, aggregate.cacheReadCostUsd,
			aggregate.cacheWriteTokens, aggregate.cacheWrite1hTok, aggregate.cacheWriteCostUsd, aggregate.thinkingTokens,
			aggregate.inputImageTokens, aggregate.outputImageTokens, aggregate.totalCostUsd,
			aggregate.durationMsSum, aggregate.durationMsCount, aggregate.durationMsMax, aggregate.firstTokenMsSum, aggregate.firstTokenMsCount,
			aggregate.lastUsedAt, lastErrorAt, updatedAt)
	}
	if err != nil {
		return fmt.Errorf("upsert %s 失败: %w", table, err)
	}
	return nil
}

// markClientIPRangeWindowDirty mirrors markClientIpRangeWindowsDirtyAsync:
// generation increments on every dirty write; the refresh CAS-deletes by the
// generation it observed.
func (s *Store) markClientIPRangeWindowDirty(ctx context.Context, tx execer, table, ipHash, updatedAt string) error {
	query := fmt.Sprintf(`
		INSERT INTO %s (ip_hash, generation, first_dirty_at, updated_at)
		VALUES (%s, 1, %s, %s)
		ON CONFLICT(ip_hash) DO UPDATE SET
		  generation = %s.generation + 1,
		  updated_at = EXCLUDED.updated_at
	`, table, s.placeholder(1), s.placeholder(2), s.placeholder(3), table)
	if _, err := tx.ExecContext(ctx, query, ipHash, updatedAt, updatedAt); err != nil {
		return fmt.Errorf("标记 %s 失败: %w", table, err)
	}
	return nil
}

// markCurrentClientIPUsageRangeWindowsStale mirrors
// markCurrentClientIpUsageRangeWindowsStaleAsync: every current window row
// in stats_job_state loses last_success_at so the next refresh with no dirty
// hashes still rebuilds stale windows.
func (s *Store) markCurrentClientIPUsageRangeWindowsStale(ctx context.Context, tx execer, updatedAt string, location *time.Location, now time.Time) error {
	windows := clientIPRangeWindowsForTimezone(location, now)
	for _, window := range windows {
		query := fmt.Sprintf(`
			INSERT INTO %s (scope_type, scope_id, job_name, last_success_at, updated_at)
			VALUES (%s, %s, %s, NULL, %s)
			ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
			  last_success_at = NULL,
			  updated_at = EXCLUDED.updated_at
		`, s.statsTable("stats_job_state"),
			s.placeholder(1), s.placeholder(2), s.placeholder(3), s.placeholder(4))
		if _, err := tx.ExecContext(ctx, query,
			clientIpRangeWindowScopeType,
			clientIpRangeWindowScopeID(window.StartDate, window.EndDate),
			clientIpRangeWindowJobName,
			updatedAt); err != nil {
			return fmt.Errorf("标记 client-ip 窗口 stale 失败: %w", err)
		}
	}
	return nil
}

func trimSpaces(value string) string {
	start := 0
	end := len(value)
	for start < end && isSpaceByte(value[start]) {
		start++
	}
	for end > start && isSpaceByte(value[end-1]) {
		end--
	}
	return value[start:end]
}

func isSpaceByte(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\v', '\f', '\r':
		return true
	}
	return false
}
