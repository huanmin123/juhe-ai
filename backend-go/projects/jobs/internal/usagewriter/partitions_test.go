package usagewriter

import (
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// partitionDDLRecorder is the injectable PartitionDDLExecutor fake: it
// records every issued DDL statement and can fail or slow down selected
// statements (mock 优先：不依赖真实 PostgreSQL)。
type partitionDDLRecorder struct {
	mu         sync.Mutex
	statements []string
	execCalls  int
	delay      time.Duration
	failFor    map[string]error
}

func (r *partitionDDLRecorder) ExecContext(ctx Ctx, query string, args ...any) (sql.Result, error) {
	r.mu.Lock()
	r.execCalls++
	delay := r.delay
	failures := r.failFor
	r.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	var execErr error
	for fragment, err := range failures {
		if strings.Contains(query, fragment) {
			execErr = err
		}
	}
	r.mu.Lock()
	r.statements = append(r.statements, query)
	r.mu.Unlock()
	if execErr != nil {
		return nil, execErr
	}
	return stubDDLResult{}, nil
}

func (r *partitionDDLRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.execCalls
}

func (r *partitionDDLRecorder) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.statements...)
}

type stubDDLResult struct{}

func (stubDDLResult) LastInsertId() (int64, error) { return 0, nil }
func (stubDDLResult) RowsAffected() (int64, error) { return 0, nil }

func TestUsageRecordPartitionDateKeyFromIso(t *testing.T) {
	cases := []struct {
		value   string
		want    string
		wantOK  bool
		reason  string
	}{
		{value: "2026-09-09T12:00:00.000Z", want: "20260909", wantOK: true, reason: "UTC 毫秒 instants 取前缀日期"},
		{value: "2026-09-09T23:59:59-05:00", want: "20260909", wantOK: true, reason: "Node 语义只看 ISO 前缀，不做时区换算"},
		{value: "  2026-09-09T00:00:00.000Z", want: "20260909", wantOK: true, reason: "Node 先 trim 再匹配"},
		{value: "2024-02-29", want: "20240229", wantOK: true, reason: "闰日有效"},
		{value: "2026-02-29", wantOK: false, reason: "非闰年 2 月 29 日被 UTC 日期复校拒绝"},
		{value: "2026-02-30", wantOK: false, reason: "2 月 30 日被 UTC 日期复校拒绝"},
		{value: "2026-13-01", wantOK: false, reason: "月份越界"},
		{value: "2026-00-10", wantOK: false, reason: "月份为 0 被 UTC 日期复校拒绝"},
		{value: "2026-09-9", wantOK: false, reason: "日必须两位数字"},
		{value: "not-a-date", wantOK: false, reason: "非日期前缀"},
		{value: "", wantOK: false, reason: "空值"},
	}
	for _, testCase := range cases {
		got, ok := usageRecordPartitionDateKeyFromIso(testCase.value)
		if ok != testCase.wantOK {
			t.Fatalf("usageRecordPartitionDateKeyFromIso(%q) ok = %v, want %v（%s）", testCase.value, ok, testCase.wantOK, testCase.reason)
		}
		if ok && got != testCase.want {
			t.Fatalf("usageRecordPartitionDateKeyFromIso(%q) = %q, want %q（%s）", testCase.value, got, testCase.want, testCase.reason)
		}
	}
}

func TestPostgresUsageRecordPartitionNameAndBounds(t *testing.T) {
	name, err := postgresUsageRecordPartitionName("20260909")
	if err != nil || name != "usage_records_20260909" {
		t.Fatalf("postgresUsageRecordPartitionName = %q, %v", name, err)
	}
	cases := []struct {
		dateKey   string
		startDate string
		endDate   string
	}{
		{dateKey: "20260909", startDate: "2026-09-09", endDate: "2026-09-10"},
		{dateKey: "20261231", startDate: "2026-12-31", endDate: "2027-01-01"},
		{dateKey: "20260131", startDate: "2026-01-31", endDate: "2026-02-01"},
		{dateKey: "20260228", startDate: "2026-02-28", endDate: "2026-03-01"},
		{dateKey: "20240229", startDate: "2024-02-29", endDate: "2024-03-01"},
	}
	for _, testCase := range cases {
		start, end, err := postgresUsageRecordPartitionBounds(testCase.dateKey)
		if err != nil {
			t.Fatalf("postgresUsageRecordPartitionBounds(%q) err = %v", testCase.dateKey, err)
		}
		if start != testCase.startDate || end != testCase.endDate {
			t.Fatalf("postgresUsageRecordPartitionBounds(%q) = (%q, %q), want (%q, %q)",
				testCase.dateKey, start, end, testCase.startDate, testCase.endDate)
		}
	}
	for _, invalid := range []string{"20260230", "20261301", "2026090", "abcdefgh", ""} {
		if _, err := postgresUsageRecordPartitionName(invalid); err == nil || !strings.Contains(err.Error(), "使用记录分区日期无效") {
			t.Fatalf("postgresUsageRecordPartitionName(%q) err = %v, want 使用记录分区日期无效", invalid, err)
		}
		if _, _, err := postgresUsageRecordPartitionBounds(invalid); err == nil || !strings.Contains(err.Error(), "使用记录分区日期无效") {
			t.Fatalf("postgresUsageRecordPartitionBounds(%q) err = %v, want 使用记录分区日期无效", invalid, err)
		}
	}
}

func TestEnsurePostgresUsageRecordPartitionsCreatesMissingPartitions(t *testing.T) {
	recorder := &partitionDDLRecorder{}
	ensured := &ensuredPartitionDateKeys{}
	createdAts := []string{
		"2026-09-09T08:00:00.000Z",
		"2026-09-10T00:30:00.000Z",
		"2026-09-09T23:59:59.999Z",
		"2026-02-30T00:00:00.000Z",
		"",
	}
	if err := ensurePostgresUsageRecordPartitions(t.Context(), recorder, ensured, createdAts); err != nil {
		t.Fatalf("ensurePostgresUsageRecordPartitions err = %v", err)
	}
	statements := recorder.recorded()
	if len(statements) != 2 {
		t.Fatalf("issued %d statements, want 2: %v", len(statements), statements)
	}
	// Node [...new Set(...)] 保留首次出现顺序。
	wantFragments := [][]string{
		{
			`CREATE TABLE IF NOT EXISTS juhe_usage."usage_records_20260909"`,
			"PARTITION OF juhe_usage.usage_records",
			"FOR VALUES FROM ('2026-09-09') TO ('2026-09-10')",
		},
		{
			`CREATE TABLE IF NOT EXISTS juhe_usage."usage_records_20260910"`,
			"PARTITION OF juhe_usage.usage_records",
			"FOR VALUES FROM ('2026-09-10') TO ('2026-09-11')",
		},
	}
	for index, fragments := range wantFragments {
		for _, fragment := range fragments {
			if !strings.Contains(statements[index], fragment) {
				t.Fatalf("statement[%d] %q missing %q", index, statements[index], fragment)
			}
		}
	}
}

func TestEnsurePostgresUsageRecordPartitionsSkipsEnsured(t *testing.T) {
	recorder := &partitionDDLRecorder{}
	ensured := &ensuredPartitionDateKeys{}
	createdAts := []string{"2026-09-09T08:00:00.000Z"}
	if err := ensurePostgresUsageRecordPartitions(t.Context(), recorder, ensured, createdAts); err != nil {
		t.Fatalf("first ensure err = %v", err)
	}
	if recorder.count() != 1 {
		t.Fatalf("first ensure issued %d DDL, want 1", recorder.count())
	}
	for batch := 0; batch < 3; batch++ {
		if err := ensurePostgresUsageRecordPartitions(t.Context(), recorder, ensured, createdAts); err != nil {
			t.Fatalf("ensure #%d err = %v", batch+2, err)
		}
	}
	if recorder.count() != 1 {
		t.Fatalf("repeat ensures issued %d DDL, want still 1（已存在日期跳过）", recorder.count())
	}
}

func TestEnsurePostgresUsageRecordPartitionsConcurrentIsIdempotent(t *testing.T) {
	recorder := &partitionDDLRecorder{delay: 5 * time.Millisecond}
	ensured := &ensuredPartitionDateKeys{}
	var group sync.WaitGroup
	errs := make([]error, 32)
	for index := range errs {
		group.Add(1)
		go func(slot int) {
			defer group.Done()
			errs[slot] = ensurePostgresUsageRecordPartitions(
				t.Context(), recorder, ensured, []string{"2026-09-09T08:00:00.000Z"})
		}(index)
	}
	group.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent ensure err = %v", err)
		}
	}
	if issued := recorder.count(); issued != 1 {
		t.Fatalf("concurrent ensures issued %d DDL, want exactly 1", issued)
	}
}

func TestEnsurePostgresUsageRecordPartitionsFailureIsNotMemoized(t *testing.T) {
	original := errors.New("no partition of relation found for row")
	recorder := &partitionDDLRecorder{failFor: map[string]error{
		"usage_records_20260909": original,
	}}
	ensured := &ensuredPartitionDateKeys{}
	err := ensurePostgresUsageRecordPartitions(
		t.Context(), recorder, ensured, []string{"2026-09-09T08:00:00.000Z"})
	if !errors.Is(err, original) {
		t.Fatalf("ensure err = %v, want 原始错误 %v 原样上抛", err, original)
	}
	if recorder.count() != 1 {
		t.Fatalf("failed ensure issued %d DDL, want 1", recorder.count())
	}
	// 失败不进备忘：下一批重试同一日期键的 DDL。
	recorder.failFor = nil
	if err := ensurePostgresUsageRecordPartitions(
		t.Context(), recorder, ensured, []string{"2026-09-09T08:00:00.000Z"}); err != nil {
		t.Fatalf("retry ensure err = %v", err)
	}
	if recorder.count() != 2 {
		t.Fatalf("retry issued %d DDL total, want 2（失败后必须重试）", recorder.count())
	}
}

func TestEnsurePostgresUsageRecordPartitionsSkipsInvalidCreatedAt(t *testing.T) {
	recorder := &partitionDDLRecorder{}
	ensured := &ensuredPartitionDateKeys{}
	if err := ensurePostgresUsageRecordPartitions(t.Context(), recorder, ensured, []string{"", "   ", "garbage"}); err != nil {
		t.Fatalf("ensure err = %v, want nil（无效 createdAt 由 INSERT 原始错误承载）", err)
	}
	if recorder.count() != 0 {
		t.Fatalf("invalid createdAts issued %d DDL, want 0", recorder.count())
	}
}

// TestEnsureChainConsumesWritePlanCreatedAts 验证 WriteBatch 接线的数据面：
// BuildWritePlan 产出的行携带 createdAt，ensure 链据此生成日期键
// （不经过真实 *sql.DB）。
func TestEnsureChainConsumesWritePlanCreatedAts(t *testing.T) {
	clock := fixedClock("2026-09-09T12:00:00.000Z")
	plan, err := BuildWritePlan(t.Context(), []UsageRecordInput{{
		SystemAccountID: "sa1",
		TrafficSource:   TrafficSourceGateway,
		Success:         true,
		CreatedAt:       "2026-09-10T03:00:00.000Z",
	}}, WritePlanOptions{Postgres: true, ShardCount: 4}, clock)
	if err != nil {
		t.Fatalf("BuildWritePlan err = %v", err)
	}
	createdAts := make([]string, 0, 1)
	for _, shardRows := range plan.RowsByShard {
		for _, row := range shardRows.Rows {
			createdAts = append(createdAts, row.CreatedAt)
		}
	}
	recorder := &partitionDDLRecorder{}
	ensured := &ensuredPartitionDateKeys{}
	if err := ensurePostgresUsageRecordPartitions(t.Context(), recorder, ensured, createdAts); err != nil {
		t.Fatalf("ensure err = %v", err)
	}
	statements := recorder.recorded()
	if len(statements) != 1 {
		t.Fatalf("issued %d statements, want 1: %v", len(statements), statements)
	}
	if !strings.Contains(statements[0], `usage_records_20260910`) ||
		!strings.Contains(statements[0], "FOR VALUES FROM ('2026-09-10') TO ('2026-09-11')") {
		t.Fatalf("statement %q does not cover the write-plan day", statements[0])
	}
}
