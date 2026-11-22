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
				malformed[20] = "2030-08-16T11:00:00Z"
				malformed[21] = nil
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
		} else if conn.mode == "qwen-authorized" {
			qwen := directInputLifecycleCandidateValues()
			qwen[31] = "qwen"
			qwen[33] = "openai"
			qwen[34] = "v1"
			qwen[35] = "api_key"
			qwen[36] = "qwen_dashscope"
			qwen[37] = "https://dashscope.aliyuncs.com"
			values = [][]driver.Value{qwen}
		} else if conn.mode == "volcengine-own" {
			volcengine := directInputLifecycleCandidateValues()
			// 自有账户臂：清空授权（24..28）/来源（29、30）投影，走账户自身
			// 身份与健康检查连接。
			for index := 24; index <= 30; index++ {
				volcengine[index] = nil
			}
			volcengine[46] = nil
			volcengine[4] = "volcengine"
			volcengine[6] = "openai"
			volcengine[7] = "v1"
			volcengine[8] = "api_key"
			volcengine[9] = "active"
			volcengine[11] = "chat_json"
			volcengine[12] = "doubao-test"
			volcengine[13] = "volcengine_ark"
			volcengine[14] = "https://ark.cn-beijing.volces.com/api/v3"
			values = [][]driver.Value{volcengine}
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
	columns := make([]string, 56)
	for index := range columns {
		columns[index] = fmt.Sprintf("c%d", index)
	}
	return columns
}

func directInputLifecycleCandidateValues() []driver.Value {
	return directInputLifecycleCandidateValuesFor("account-rows-lifecycle")
}

// directInputLifecycleCandidateValuesFor 的列序与 directInputCandidatesSQL 的
// SELECT 投影逐列对齐（供应商解耦形态 + 后台探测开关两列）：身份
// vendor_code/preset，连接面 preset/base_url 位于 13/14，expedited_recovery_enabled
// 位于 index 19，cooldown fence 位于 20/21，a.background_probe_enabled 位于 23，
// 授权实例五列位于 24..28（limits_json 与 effective_source_team_id 随授权额度
// 域退役摘除），source 身份九列位于 29..37，source 状态七列位于 38..44（44 为
// COALESCE 的 source_background_probe_enabled），binding 两列 45/46，proxy 七列
// 47..53，池绑定两列（a.proxy_pool_id/source.proxy_pool_id）位于 54/55（共 56
// 列；本夹具为直接代理候选，池列为 NULL，不触发池解析路径）。
func directInputLifecycleCandidateValuesFor(accountID string) []driver.Value {
	secret := "direct-input-rows-lifecycle-secret"
	credentials, err := exactkeyprobe.EncryptV1Envelope(secret, []byte(`{"api_keys":["sk-test"]}`))
	if err != nil {
		panic(err)
	}
	values := make([]driver.Value, 56)
	values[0] = accountID
	values[1] = int64(1)
	values[2] = int64(2)
	values[3] = int64(3)
	values[4] = "gpt" // a.vendor_code
	values[5] = nil   // 有效 oauth_type（CASE 投影）
	values[6] = "openai"
	values[7] = "v1"
	values[8] = "api_key"
	values[9] = "active"
	values[10] = int64(1)
	values[11] = "chat_json" // 健康检查 mode（CASE 投影）
	values[12] = "gpt-test"  // 健康检查模型（CASE 投影）
	values[13] = "gpt_openai_v1"
	values[14] = "https://api.example.com"
	values[15] = credentials
	values[18] = false
	values[19] = int64(0)
	values[22] = "system-account"
	values[23] = int64(1) // a.background_probe_enabled
	values[24] = "authorization-1"
	values[25] = "active"
	values[27] = "source-account" // ra.resource_id
	values[28] = "owner-account"  // ra.resource_owner_system_account_id
	values[29] = "source-account" // source.id
	values[30] = int64(4)         // source.config_revision
	values[31] = "gpt"            // source.vendor_code
	values[32] = nil              // source.oauth_type
	values[33] = "openai"
	values[34] = "v1"
	values[35] = "api_key"
	values[36] = "gpt_openai_v1"
	values[37] = "https://source.example.com"
	values[38] = "active"
	values[39] = int64(1)
	values[43] = credentials
	values[44] = int64(1) // source_background_probe_enabled（COALESCE 投影）
	values[45] = "group-1"
	values[46] = "authorization-1"
	values[47] = "proxy-1"
	values[48] = true
	values[49] = "http"
	values[50] = "127.0.0.1"
	values[51] = int64(8080)
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
	if !strings.Contains(directInputCandidatesSQL, "hc.base_url") {
		t.Fatal("PG direct input 候选查询必须携带健康检查连接 base_url")
	}
	for _, mode := range []string{"messages_json", "messages_sse", "generate_content_json", "generate_content_sse", "interactions_json", "interactions_sse"} {
		if !strings.Contains(directInputCandidatesSQL, "'"+mode+"'") {
			t.Fatalf("PG direct input 候选查询必须包含 %s", mode)
		}
	}
	// 供应商解耦：health-check mode 单源账户/来源账户行（mapping LATERAL 随
	// account_model_mappings 退役，stream_generate_content 家族映射一并删除），
	// 探针身份由健康检查连接 preset 派生。
	for _, column := range []string{"protocol_code", "protocol_version"} {
		if !strings.Contains(directInputCandidatesSQL, "a."+column) || !strings.Contains(directInputCandidatesSQL, "source."+column) {
			t.Fatalf("PG direct input 查询必须冻结账户和来源的 %s", column)
		}
	}
	if strings.Contains(directInputCandidatesSQL, "provider_protocol_profile_id") || strings.Contains(directInputCandidatesSQL, "account_model_mappings") {
		t.Fatal("PG direct input 查询不得再读取已删除的 profile/mapping 旧列旧表")
	}
	if !strings.Contains(directInputCandidatesSQL, "hc.connection_preset_code") || !strings.Contains(directInputCandidatesSQL, "source_hc.connection_preset_code") {
		t.Fatal("探针身份必须来自健康检查连接 preset（含授权实例来源投影）")
	}
	for _, column := range []string{"vendor_code", "oauth_type"} {
		if !strings.Contains(directInputCandidatesSQL, "a."+column) || !strings.Contains(directInputCandidatesSQL, "source."+column) {
			t.Fatalf("PG direct input 查询必须冻结账户和来源的 %s", column)
		}
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

// TestDirectInputCandidatesExcludeNonConversationAccounts 锁定 J1 只承载
// 真实对话形态的模型检查：图像等非对话账户不得进入 J1 候选（由
// account-connectivity-health 连通性通道负责），也不得保留图像形态或
// 图像周期性跳过特判。
func TestDirectInputCandidatesExcludeNonConversationAccounts(t *testing.T) {
	if strings.Contains(directInputCandidatesSQL, "images_json") {
		t.Fatalf("J1 候选 SQL 不得再出现 images_json（非对话账户改由连通性健康检查承载）: %s", directInputCandidatesSQL)
	}
	// 显式账户装载仍可绕过周期性到期门控（$3），该分支与能力域无关。
	if !strings.Contains(directInputCandidatesSQL, "$3::boolean") {
		t.Fatal("explicit account loads must be able to bypass periodic due scheduling")
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
	base := exactkeyprobe.DirectInput{Account: exactkeyprobe.DirectAccount{ID: "bad-proxy-account", ConfigRevision: 2, DispatchRevision: 3, Provider: "gpt", Type: "api_key", Status: "pending_test", EndpointMode: "chat_json", HealthModel: "gpt-test", CredentialsEncrypted: credentials, ConnectionBaseURL: "https://api.example.com/v1"}, Binding: exactkeyprobe.DirectBinding{GroupID: "group-1", Enabled: true}, InputVersion: 4, IssuedAt: now, ExpiresAt: now.Add(time.Hour), TLSPolicy: "j1-direct-upstream-v1", Schedule: exactkeyprobe.Schedule{HealthIntervalMS: 1, FailureThreshold: 1, FailureRetryMS: 1, CooldownNeutralBaseMS: 1, CooldownNeutralMaxMS: 1, CooldownFailureBackoffMS: 1}}
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

// TestDirectInputJ1MappingCoversConversationVendors 锁定 J1 专用
// preset→profile 映射：共享 10 对对话 preset 原样覆盖，并新增
// volcengine_ark/qwen_dashscope 扩展对；正反向一致，共享映射口径不变。
func TestDirectInputJ1MappingCoversConversationVendors(t *testing.T) {
	expected := []struct{ preset, profile string }{
		{"gpt_openai_v1", "profile_gpt_openai_v1"},
		{"xai_openai_v1", "profile_xai_openai_v1"},
		{"deepseek_openai_v1", "profile_deepseek_openai_v1"},
		{"deepseek_anthropic_v1", "profile_deepseek_anthropic_v1"},
		{"anthropic_v1", "profile_anthropic_anthropic_v1"},
		{"glm_general_openai_v1", "profile_glm_general_openai_v1"},
		{"glm_coding_openai_v1", "profile_glm_coding_openai_v1"},
		{"glm_coding_anthropic_v1", "profile_glm_coding_anthropic_v1"},
		{"gemini_openai_chat_v1beta", "profile_gemini_openai_chat_v1beta"},
		{"gemini_native_v1beta", "profile_gemini_native_v1beta"},
		{"volcengine_ark", "profile_volcengine_openai_v1"},
		{"qwen_dashscope", "profile_qwen_openai_v1"},
	}
	for _, item := range expected {
		profile, ok := directInputProfileIDForPreset(item.preset)
		if !ok || profile != item.profile {
			t.Fatalf("J1 forward %q = %q/%v, want %q", item.preset, profile, ok, item.profile)
		}
		preset, ok := directInputPresetForProfileID(item.profile)
		if !ok || preset != item.preset {
			t.Fatalf("J1 reverse %q = %q/%v, want %q", item.profile, preset, ok, item.preset)
		}
	}
	if _, ok := directInputProfileIDForPreset("minimax_media"); ok {
		t.Fatal("纯媒体 preset 不得进入 J1 映射")
	}
	// J3a/读面共享映射必须保持 10 对：J1 扩展不得写回共享表。
	if _, ok := exactkeyprobe.ConnectionPresetProfileID("volcengine_ark"); ok {
		t.Fatal("共享映射不得因 J1 扩面而派生 volcengine_ark")
	}
	if _, ok := exactkeyprobe.ConnectionPresetProfileID("qwen_dashscope"); ok {
		t.Fatal("共享映射不得因 J1 扩面而派生 qwen_dashscope")
	}
	if _, ok := exactkeyprobe.ConnectionPresetForProfileID("profile_qwen_openai_v1"); ok {
		t.Fatal("共享反向映射不得因 J1 扩面而命中 qwen profile")
	}
}

// TestDirectInputCandidatesCoverVolcengineAndQwen 锁定候选 SQL 两处 preset
// 白名单（账户/来源）都包含两个对话厂商，且健康检查关系已纳入只读契约。
func TestDirectInputCandidatesCoverVolcengineAndQwen(t *testing.T) {
	for _, preset := range []string{"volcengine_ark", "qwen_dashscope"} {
		if count := strings.Count(directInputCandidatesSQL, "'"+preset+"'"); count != 2 {
			t.Fatalf("候选 SQL 的 preset %q 应同时出现在账户/来源白名单（2 次），实际 %d", preset, count)
		}
	}
	found := false
	for _, relation := range directInputRequiredRelations {
		if relation == "juhe_business.account_connections" {
			found = true
		}
	}
	if !found {
		t.Fatal("J1 只读契约必须包含 account_connections（候选 SQL 实际引用）")
	}
}

// TestDirectInputBaselineCoversConversationVendorsAndInstanceSource 锁定基线
// 播种：preset 数组含两个新增对话 preset，连接 EXISTS 按资源账户回退
// （授权实例取 source，普通账户取自身；source 为空时 NULL 不匹配）。
func TestDirectInputBaselineCoversConversationVendorsAndInstanceSource(t *testing.T) {
	if len(directInputBaselineConnectionPresetCodes) != 12 {
		t.Fatalf("基线 preset 数组长度 = %d, want 12（共享 10 对 + 2 个 J1 扩展）", len(directInputBaselineConnectionPresetCodes))
	}
	presets := make(map[string]bool, len(directInputBaselineConnectionPresetCodes))
	for _, preset := range directInputBaselineConnectionPresetCodes {
		presets[preset] = true
		if !strings.Contains(directInputCandidatesSQL, "'"+preset+"'") {
			t.Fatalf("基线 preset %q 不在候选 SQL 白名单中（两处白名单必须同步）", preset)
		}
	}
	for _, preset := range []string{"volcengine_ark", "qwen_dashscope"} {
		if !presets[preset] {
			t.Fatalf("基线 preset 数组缺少 %q", preset)
		}
	}
	query := directInputBaselineSQL()
	if !strings.Contains(query, "CASE WHEN a.authorization_instance_authorization_id IS NULL THEN a.id ELSE a.authorization_instance_source_account_id END") {
		t.Fatalf("基线连接 EXISTS 必须按资源账户回退（实例取 source）：%s", query)
	}
	if strings.Contains(query, "c.account_id = a.id AND c.enabled") {
		t.Fatal("基线连接 EXISTS 不得只看实例自身连接（授权实例无自身连接）")
	}
	for _, preset := range []string{"volcengine_ark", "qwen_dashscope"} {
		if !strings.Contains(query, "'"+preset+"'") {
			t.Fatalf("基线 SQL preset 白名单缺少 %q", preset)
		}
	}
}

// TestPostgresDirectInputReaderAcceptsVolcengineOwnAccount 覆盖自有账户的
// volcengine 候选扫描：J1 映射派生 profile、base 直通、ToInput 成功。
func TestPostgresDirectInputReaderAcceptsVolcengineOwnAccount(t *testing.T) {
	registerDirectInputRowsLifecycleDriver.Do(func() {
		sql.Register(directInputRowsLifecycleDriverName, directInputRowsLifecycleDriver{})
	})
	database, err := sql.Open(directInputRowsLifecycleDriverName, "volcengine-own")
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
		t.Fatalf("volcengine direct input load: %v", err)
	}
	if len(result.Failures) != 0 || len(result.Inputs) != 1 {
		t.Fatalf("volcengine result = %#v", result)
	}
	input := result.Inputs[0]
	if input.Provider != "volcengine" || input.ProtocolProfileID != "profile_volcengine_openai_v1" ||
		input.BaseURL != "https://ark.cn-beijing.volces.com/api/v3" || input.HealthModel != "doubao-test" || input.EndpointMode != "chat_json" {
		t.Fatalf("volcengine input = %#v", input)
	}
}

// TestPostgresDirectInputReaderAcceptsQwenAuthorizedInstance 覆盖授权实例的
// qwen 候选扫描：有效来源投影派生 profile 与 origin 服务根，ToInput 成功。
func TestPostgresDirectInputReaderAcceptsQwenAuthorizedInstance(t *testing.T) {
	registerDirectInputRowsLifecycleDriver.Do(func() {
		sql.Register(directInputRowsLifecycleDriverName, directInputRowsLifecycleDriver{})
	})
	database, err := sql.Open(directInputRowsLifecycleDriverName, "qwen-authorized")
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
		t.Fatalf("qwen authorized direct input load: %v", err)
	}
	if len(result.Failures) != 0 || len(result.Inputs) != 1 {
		t.Fatalf("qwen result = %#v", result)
	}
	input := result.Inputs[0]
	if input.Provider != "qwen" || input.ProtocolProfileID != "profile_qwen_openai_v1" ||
		input.BaseURL != "https://dashscope.aliyuncs.com/compatible-mode/v1" || input.EndpointMode != "chat_json" {
		t.Fatalf("qwen input = %#v", input)
	}
}
