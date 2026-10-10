package accounthealth

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

const directInputRowsLifecycleDriverName = "accounthealth-direct-input-rows-lifecycle"

var registerDirectInputRowsLifecycleDriver sync.Once

type directInputRowsLifecycleDriver struct{}

func (directInputRowsLifecycleDriver) Open(name string) (driver.Conn, error) {
	return &directInputRowsLifecycleConn{mode: name}, nil
}

type directInputRowsLifecycleConn struct {
	mu                sync.Mutex
	candidateRowsOpen bool
	mode              string
}

func (*directInputRowsLifecycleConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("prepared statements are not supported by the test driver")
}

func (*directInputRowsLifecycleConn) Close() error { return nil }

func (conn *directInputRowsLifecycleConn) Begin() (driver.Tx, error) {
	return &directInputRowsLifecycleTx{conn: conn}, nil
}

func (conn *directInputRowsLifecycleConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return conn.Begin()
}

func (conn *directInputRowsLifecycleConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return directInputRowsLifecycleResult{}, nil
}

func (conn *directInputRowsLifecycleConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if conn.candidateRowsOpen {
		return nil, fmt.Errorf("candidate rows must be closed before a second query")
	}
	if strings.Contains(query, "FROM juhe_business.accounts a") {
		conn.candidateRowsOpen = true
		values := [][]driver.Value{directInputLifecycleCandidateValues()}
		if conn.mode == "malformed" {
			offset := int64(0)
			if len(args) >= 5 {
				offset, _ = args[4].Value.(int64)
			}
			switch offset {
			case 0:
				malformed := directInputLifecycleCandidateValues()
				malformed[0] = "account-malformed-fence"
				malformed[21] = "2030-08-16T11:00:00Z"
				malformed[22] = nil
				values = [][]driver.Value{malformed}
			case 1:
				values = [][]driver.Value{directInputLifecycleCandidateValues()}
			default:
				values = [][]driver.Value{}
			}
		} else if conn.mode == "scan-error" {
			invalid := directInputLifecycleCandidateValues()
			invalid[1] = "not-an-input-version"
			values = [][]driver.Value{invalid}
		}
		return &directInputRowsLifecycleRows{conn: conn, candidate: true, columns: directInputLifecycleCandidateColumns(), values: values}, nil
	}
	if strings.Contains(query, "SELECT key, value_json") {
		return &directInputRowsLifecycleRows{columns: []string{"key", "value_json"}, values: directInputLifecycleSettingsValues()}, nil
	}
	return &directInputRowsLifecycleRows{columns: []string{"value"}}, nil
}

type directInputRowsLifecycleTx struct {
	conn *directInputRowsLifecycleConn
}

func (*directInputRowsLifecycleTx) Commit() error   { return nil }
func (*directInputRowsLifecycleTx) Rollback() error { return nil }

type directInputRowsLifecycleResult struct{}

func (directInputRowsLifecycleResult) LastInsertId() (int64, error) { return 0, nil }
func (directInputRowsLifecycleResult) RowsAffected() (int64, error) { return 0, nil }

type directInputRowsLifecycleRows struct {
	conn      *directInputRowsLifecycleConn
	candidate bool
	columns   []string
	values    [][]driver.Value
	index     int
}

func (rows *directInputRowsLifecycleRows) Columns() []string { return rows.columns }

func (rows *directInputRowsLifecycleRows) Close() error {
	if rows.candidate {
		rows.conn.mu.Lock()
		rows.conn.candidateRowsOpen = false
		rows.conn.mu.Unlock()
	}
	return nil
}
func (rows *directInputRowsLifecycleRows) Next(dest []driver.Value) error {
	if rows.index >= len(rows.values) {
		if rows.candidate {
			rows.conn.mu.Lock()
			rows.conn.candidateRowsOpen = false
			rows.conn.mu.Unlock()
		}
		return io.EOF
	}
	copy(dest, rows.values[rows.index])
	rows.index++
	return nil
}

func directInputLifecycleCandidateColumns() []string {
	columns := make([]string, 54)
	for index := range columns {
		columns[index] = fmt.Sprintf("c%d", index)
	}
	return columns
}

func directInputLifecycleCandidateValues() []driver.Value {
	return directInputLifecycleCandidateValuesFor("account-rows-lifecycle")
}

// directInputLifecycleCandidateValuesFor 的列序与 directInputCandidatesSQL 的
// SELECT 投影逐列对齐；expedited_recovery_enabled 位于 index 20，其后的账户
// /授权/来源/binding/proxy 列全部顺延一列。
func directInputLifecycleCandidateValuesFor(accountID string) []driver.Value {
	secret := "direct-input-rows-lifecycle-secret"
	credentials, err := exactkeyprobe.EncryptV1Envelope(secret, []byte(`{"api_keys":["sk-test"],"base_url":"https://api.example.com"}`))
	if err != nil {
		panic(err)
	}
	values := make([]driver.Value, 54)
	values[0] = accountID
	values[1] = int64(1)
	values[2] = int64(2)
	values[3] = int64(3)
	values[4] = "openai"
	values[5] = "profile_openai_openai_v1"
	values[6] = "openai"
	values[7] = "v1"
	values[8] = "api_key"
	values[9] = "openai_standard"
	values[10] = "active"
	values[11] = int64(1)
	values[12] = "chat_json"
	values[13] = "gpt-test"
	values[16] = credentials
	values[19] = false
	values[20] = int64(0)
	values[23] = "system-account"
	values[24] = "authorization-1"
	values[25] = "active"
	values[27] = `{}`
	values[28] = "source-account"
	values[29] = "owner-account"
	values[31] = "source-account"
	values[32] = int64(4)
	values[33] = "openai"
	values[34] = "profile_openai_openai_v1"
	values[35] = "openai"
	values[36] = "v1"
	values[37] = "api_key"
	values[38] = "openai_standard"
	values[39] = "active"
	values[40] = int64(1)
	values[44] = credentials
	values[45] = "group-1"
	values[46] = "authorization-1"
	return values
}

func directInputLifecycleSettingsValues() [][]driver.Value {
	return [][]driver.Value{
		{"accountHealthCheckIntervalHours", "1"},
		{"accountHealthCheckJitterMinutes", "0"},
		{"accountHealthCheckFailureThreshold", "1"},
		{"defaultTemporaryUnschedulableMinutes", "5"},
		{"cooldownAccountRetestMaxBackoffHours", "24"},
		{"usageStatsTimezone", `"UTC"`},
	}
}

func TestPostgresDirectInputReaderClosesCandidateRowsBeforeQuotaQueries(t *testing.T) {
	registerDirectInputRowsLifecycleDriver.Do(func() {
		sql.Register(directInputRowsLifecycleDriverName, directInputRowsLifecycleDriver{})
	})
	database, err := sql.Open(directInputRowsLifecycleDriverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)
	now := time.Date(2030, 8, 16, 12, 0, 0, 0, time.UTC)
	reader, err := NewPostgresDirectInputReader(database, "direct-input-rows-lifecycle-secret", time.Hour, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.LoadDueWithFailures(context.Background(), 1)
	if err != nil {
		t.Fatalf("direct input load must permit quota queries after candidate rows close: %v", err)
	}
	if len(result.Inputs) != 1 || result.Inputs[0].AccountID != "account-rows-lifecycle" {
		t.Fatalf("unexpected direct input result: %#v", result)
	}
}

func TestPostgresDirectInputReaderIsolatesMalformedCooldownFence(t *testing.T) {
	registerDirectInputRowsLifecycleDriver.Do(func() {
		sql.Register(directInputRowsLifecycleDriverName, directInputRowsLifecycleDriver{})
	})
	database, err := sql.Open(directInputRowsLifecycleDriverName, "malformed")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)
	now := time.Date(2030, 8, 16, 12, 0, 0, 0, time.UTC)
	reader, err := NewPostgresDirectInputReader(database, "direct-input-rows-lifecycle-secret", time.Hour, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.LoadDueWithFailures(context.Background(), 1)
	if err != nil {
		t.Fatalf("malformed cooldown fence must be isolated to its candidate: %v", err)
	}
	if len(result.Failures) != 1 {
		t.Fatalf("malformed candidate failures = %#v", result.Failures)
	}
	if got := result.Failures[0]; got.AccountID != "account-malformed-fence" || got.InputVersion != 1 || got.ConfigRevision != 2 || got.DispatchRevision != 3 {
		t.Fatalf("malformed candidate failure fence = %#v", got)
	}
	if len(result.Inputs) != 1 || result.Inputs[0].AccountID != "account-rows-lifecycle" {
		t.Fatalf("valid candidate must continue after malformed row: %#v", result.Inputs)
	}
}

func TestPostgresDirectInputReaderKeepsScanErrorsFailClosed(t *testing.T) {
	registerDirectInputRowsLifecycleDriver.Do(func() {
		sql.Register(directInputRowsLifecycleDriverName, directInputRowsLifecycleDriver{})
	})
	database, err := sql.Open(directInputRowsLifecycleDriverName, "scan-error")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)
	now := time.Date(2030, 8, 16, 12, 0, 0, 0, time.UTC)
	reader, err := NewPostgresDirectInputReader(database, "direct-input-rows-lifecycle-secret", time.Hour, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.LoadDueWithFailures(context.Background(), 1)
	if err == nil || !strings.Contains(err.Error(), "解码 PG direct input 候选失败") {
		t.Fatalf("rows.Scan failure must fail the whole read: result=%#v err=%v", result, err)
	}
	if len(result.Inputs) != 0 || len(result.Failures) != 0 {
		t.Fatalf("failed scan must not produce partial results: %#v", result)
	}
}

func TestDirectInputRequiredRelationsStayOutsideJobsSchema(t *testing.T) {
	if len(directInputRequiredRelations) == 0 {
		t.Fatal("direct input contract must declare its read-only business relations")
	}
	for _, relation := range directInputRequiredRelations {
		if strings.HasPrefix(relation, "juhe_jobs.") || !strings.Contains(relation, ".") {
			t.Fatalf("direct input relation %q must remain outside the jobs-owned schema", relation)
		}
	}
}

func TestDirectInputCandidatesIncludeResponsesSSE(t *testing.T) {
	if !strings.Contains(directInputCandidatesSQL, "a.status IN ('pending_test', 'temporary_unavailable', 'rate_limited') OR a.schedulable = 1") {
		t.Fatal("due cooldown accounts must remain probeable even when a legacy schedulable bit is false")
	}
	if !strings.Contains(directInputCandidatesSQL, "'responses_sse'") {
		t.Fatal("PG direct input 候选查询必须包含 responses_sse")
	}
	if !strings.Contains(directInputCandidatesSQL, "'responses_json'") {
		t.Fatal("PG direct input 候选查询必须保留 responses_json")
	}
	for _, mode := range []string{"messages_json", "messages_sse", "generate_content_json", "generate_content_sse", "interactions_json", "interactions_sse"} {
		if !strings.Contains(directInputCandidatesSQL, "'"+mode+"'") {
			t.Fatalf("PG direct input 候选查询必须包含 %s", mode)
		}
	}
	if !strings.Contains(directInputCandidatesSQL, "generate_content_sse' THEN 'stream_generate_content'") {
		t.Fatal("Gemini GenerateContent SSE 必须按 stream_generate_content 查找模型映射")
	}
	if !strings.Contains(directInputCandidatesSQL, "mm.upstream_model <> mm.source_model OR mm.upstream_endpoint_family <> mm.source_endpoint_family") {
		t.Fatal("PG direct input 不得接受 identity model mapping")
	}
	for _, column := range []string{"provider_protocol_profile_id", "protocol_code", "protocol_version"} {
		if !strings.Contains(directInputCandidatesSQL, "a."+column) || !strings.Contains(directInputCandidatesSQL, "source."+column) {
			t.Fatalf("PG direct input 查询必须冻结账户和来源的 %s", column)
		}
	}
	if !strings.Contains(directInputCandidatesSQL, "CASE WHEN a.authorization_instance_authorization_id IS NULL THEN a.id ELSE source.id END") {
		t.Fatal("authorized hybrid mapping must read the physical source account mapping")
	}
}

func TestDirectInputCandidatesPrioritizeOverdueSchedulesBeforeRecentUpdates(t *testing.T) {
	const expectedOrder = "ORDER BY CASE WHEN a.status = 'pending_test' THEN 0 ELSE 1 END,"
	if !strings.Contains(directInputCandidatesSQL, expectedOrder) {
		t.Fatalf("PG direct input candidates must prioritize activation before periodic and cooldown checks: %s", directInputCandidatesSQL)
	}
	if !strings.Contains(directInputCandidatesSQL, "ROW_NUMBER() OVER") || !strings.Contains(directInputCandidatesSQL, "PARTITION BY CASE WHEN a.status IN ('temporary_unavailable', 'rate_limited') THEN 0 ELSE 1 END") {
		t.Fatal("active and cooldown candidates must use separate row-number partitions to prevent starvation")
	}
	if !strings.Contains(directInputCandidatesSQL, "THEN a.cooldown_until ELSE a.next_health_check_at END ASC NULLS FIRST") {
		t.Fatal("cooldown candidates must use cooldown_until as their due-order key")
	}
	orderBy := directInputCandidatesSQL[strings.LastIndex(directInputCandidatesSQL, "ORDER BY"):]
	if strings.Contains(orderBy, "updated_at") {
		t.Fatal("updated_at must not participate in direct-input scheduling, or full batches can starve overdue accounts")
	}
	if !strings.Contains(directInputCandidatesSQL, "LIMIT $2") {
		t.Fatal("direct input candidate query must preserve its bounded database limit")
	}
	if !strings.Contains(directInputCandidatesSQL, "OFFSET $5") {
		t.Fatal("direct input candidate query must support a stable refill offset after quota filtering")
	}
}

func TestDirectInputCandidatesExcludeHealthyImagesFromPeriodicScan(t *testing.T) {
	guard := "AND ($3::boolean OR a.status <> 'active' OR a.health_check_endpoint_mode <> 'images_json')"
	guardIndex := strings.Index(directInputCandidatesSQL, guard)
	dueIndex := strings.Index(directInputCandidatesSQL, "a.next_health_check_at <= $1")
	if guardIndex < 0 || dueIndex < 0 || guardIndex > dueIndex {
		t.Fatalf("periodic candidate SQL must exclude active image accounts before due scheduling: guard=%d due=%d", guardIndex, dueIndex)
	}
	if !strings.Contains(directInputCandidatesSQL, "$3::boolean") {
		t.Fatal("explicit account loads must be able to bypass the periodic image exclusion")
	}
}

func TestDirectInputCandidateFairSlotsAdmitActiveAndCooldown(t *testing.T) {
	// The SQL row-number partitions are intentionally interleaved by rank. With
	// both classes backlogged, every bounded page must admit both classes rather
	// than consuming the entire LIMIT from whichever class sorts first.
	got := fairCandidateStatusSlots(6, 8, 8)
	want := []string{"cooldown", "active", "cooldown", "active", "cooldown", "active"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("fair candidate slots = %v, want %v", got, want)
	}
	got = fairCandidateStatusSlots(6, 1, 8)
	want = []string{"cooldown", "active", "active", "active", "active", "active"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("one-sided cooldown slots = %v, want %v", got, want)
	}
}

func fairCandidateStatusSlots(limit, cooldownCount, activeCount int) []string {
	if limit <= 0 {
		return nil
	}
	result := make([]string, 0, limit)
	for rank := 0; len(result) < limit && (rank < cooldownCount || rank < activeCount); rank++ {
		if rank < cooldownCount {
			result = append(result, "cooldown")
			if len(result) == limit {
				break
			}
		}
		if rank < activeCount {
			result = append(result, "active")
		}
	}
	return result
}

func TestCollectDirectCandidatePagesRefillsAfterQuotaFilteredPage(t *testing.T) {
	const pageSize = 3
	var offsets []int
	accepted := 0 // The first page is entirely quota-ineligible.
	err := collectDirectCandidatePages(pageSize, func(offset int) (int, error) {
		offsets = append(offsets, offset)
		switch offset {
		case 0:
			if accepted != 0 {
				t.Fatalf("first full page must be entirely filtered, accepted = %d", accepted)
			}
			return pageSize, nil
		case pageSize:
			accepted += 2
			return pageSize, nil
		case 2 * pageSize:
			accepted++
			return 1, nil
		default:
			t.Fatalf("unexpected candidate page offset %d", offset)
			return 0, nil
		}
	}, func() int { return accepted })
	if err != nil {
		t.Fatalf("collect pages: %v", err)
	}
	if accepted != 3 {
		t.Fatalf("accepted = %d, want 3", accepted)
	}
	if got, want := fmt.Sprint(offsets), "[0 3 6]"; got != want {
		t.Fatalf("page offsets = %s, want %s", got, want)
	}
}

func TestCollectDirectCandidatePagesDoesNotCountMalformedCandidates(t *testing.T) {
	const pageSize = 2
	var offsets []int
	accepted := 0
	err := collectDirectCandidatePages(pageSize, func(offset int) (int, error) {
		offsets = append(offsets, offset)
		switch offset {
		case 0:
			// Both rows are malformed, but they must not consume the two-input
			// success window.
			return pageSize, nil
		case pageSize:
			accepted = pageSize
			return pageSize, nil
		default:
			t.Fatalf("unexpected candidate page offset %d", offset)
			return 0, nil
		}
	}, func() int { return accepted })
	if err != nil {
		t.Fatalf("collect pages: %v", err)
	}
	if got, want := fmt.Sprint(offsets), "[0 2]"; got != want {
		t.Fatalf("page offsets = %s, want %s", got, want)
	}
}

func TestCollectDirectCandidatePagesStopsAtBoundedScanCap(t *testing.T) {
	const pageSize = 2
	var offsets []int
	err := collectDirectCandidatePagesWithCap(pageSize, 4, func(offset int) (int, error) {
		offsets = append(offsets, offset)
		return pageSize, nil
	}, func() int { return 0 })
	if err != nil {
		t.Fatalf("collect pages: %v", err)
	}
	if got, want := fmt.Sprint(offsets), "[0 2]"; got != want {
		t.Fatalf("bounded scan offsets = %s, want %s", got, want)
	}
}

func TestDirectInputScanCapAllowsCooldownBacklogToDrain(t *testing.T) {
	if got, want := directInputScanCap(64), 1024; got != want {
		t.Fatalf("production direct-input scan cap = %d, want %d", got, want)
	}
	if got, want := directInputScanCap(1000), maxDirectInputScanCandidates; got != want {
		t.Fatalf("scan cap must remain bounded at %d, got %d", want, got)
	}
}

func TestCollectDirectCandidatePagesRejectsInvalidPageSize(t *testing.T) {
	err := collectDirectCandidatePages(3, func(int) (int, error) {
		return 4, nil
	}, func() int { return 0 })
	if err == nil {
		t.Fatal("page count above the SQL limit must fail closed")
	}
}

func TestDirectInputCandidatesQuerySuppressesExactFencedGenerationBeforeLimit(t *testing.T) {
	nextDue := time.Date(2030, 8, 16, 12, 5, 0, 0, time.UTC)
	query, args := directInputCandidatesQuery([]DirectInputSuppression{{
		AccountID: "bad-account", InputVersion: 4, ConfigRevision: 5, DispatchRevision: 6, NextDueAt: nextDue,
	}})
	clause := strings.Index(query, "NOT EXISTS")
	limit := strings.Index(query, "LIMIT $2")
	if clause < 0 || limit < 0 || clause > limit {
		t.Fatalf("suppression must be applied before bounded SQL window: clause=%d limit=%d", clause, limit)
	}
	if len(args) != 5 || args[0] != "bad-account" || args[1] != int64(4) || args[2] != int64(5) || args[3] != int64(6) {
		t.Fatalf("suppression args = %#v", args)
	}
	if got, ok := args[4].(time.Time); !ok || !got.Equal(nextDue) {
		t.Fatalf("suppression due arg = %#v", args[4])
	}
	for _, expected := range []string{"$6::text", "$7::bigint", "$8::bigint", "$9::bigint", "$10::timestamptz", "iv.current_version::bigint", "a.config_revision::bigint", "a.dispatch_revision::bigint", "$1::timestamptz"} {
		if !strings.Contains(query, expected) {
			t.Fatalf("suppression query must pin PostgreSQL parameter type %q: %s", expected, query)
		}
	}
}

func TestBuildDirectCandidateInputIsolatesBadProxyWithoutLeakingSecrets(t *testing.T) {
	secret := "j1-direct-input-secret"
	credentials, err := exactkeyprobe.EncryptV1Envelope(secret, []byte(`{"api_key":"key"}`))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 8, 16, 0, 0, 0, 0, time.UTC)
	base := exactkeyprobe.DirectInput{Account: exactkeyprobe.DirectAccount{ID: "bad-proxy-account", ConfigRevision: 2, DispatchRevision: 3, Provider: "openai", Type: "api_key", Status: "pending_test", EndpointMode: "chat_json", HealthModel: "gpt-test", CredentialsEncrypted: credentials}, Binding: exactkeyprobe.DirectBinding{GroupID: "group-1", Enabled: true}, InputVersion: 4, IssuedAt: now, ExpiresAt: now.Add(time.Hour), TLSPolicy: "j1-direct-upstream-v1", Schedule: exactkeyprobe.Schedule{HealthIntervalMS: 1, FailureThreshold: 1, FailureRetryMS: 1, CooldownNeutralBaseMS: 1, CooldownNeutralMaxMS: 1, CooldownFailureBackoffMS: 1}}
	bad := base
	bad.Proxy = &exactkeyprobe.DirectProxy{ID: "bad-proxy", Enabled: false, Type: "http", Host: "127.0.0.1", Port: 8080, PasswordEncrypted: "must-not-escape"}
	candidate := directCandidate{account: bad.Account, inputVersion: bad.InputVersion}
	if input, failure, err := buildDirectCandidateInput(candidate, bad, secret, now); err != nil || input.AccountID != "" || failure == nil || failure.AccountID != bad.Account.ID || failure.InputVersion != 4 || failure.ConfigRevision != 2 || failure.DispatchRevision != 3 {
		t.Fatalf("bad candidate must become a fenced failure: input=%#v failure=%#v err=%v", input, failure, err)
	}
	good := base
	good.Account.ID = "good-account"
	candidate = directCandidate{account: good.Account, inputVersion: good.InputVersion}
	input, failure, err := buildDirectCandidateInput(candidate, good, secret, now)
	if err != nil || failure != nil || input.AccountID != good.Account.ID {
		t.Fatalf("normal candidate after isolated bad candidate must stay probeable: input=%#v failure=%#v err=%v", input, failure, err)
	}
}
