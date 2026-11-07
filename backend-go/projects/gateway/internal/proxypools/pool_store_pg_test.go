package proxypools

// 真实 PG 契约测试（代理池设计 §8.1/§4.5/§6.2）：schema 列型照抄
// maintenance 生产业务 DDL 片段（pg_schema_business_tables.go）。外部依赖
// 授权门由 pgtestkit 统一执行：缺 JUHE_AI_TEST_EXTERNAL=1 时整组跳过，
// 离线回归由本包纯逻辑测试与 go build 兜底。

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// poolSchema 照抄生产 DDL（timestamptz、CHECK、UNIQUE(pool_id,priority)、
// generation 序列与租约复合 FK）。
const poolSchema = `
	CREATE SCHEMA IF NOT EXISTS juhe_business;
	CREATE SEQUENCE IF NOT EXISTS juhe_business.account_proxy_lease_generation_seq;
	CREATE TABLE juhe_business.proxy_profiles (
		id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT,
		type TEXT NOT NULL, host TEXT NOT NULL, port INTEGER NOT NULL, username TEXT,
		password_encrypted TEXT, enabled BOOLEAN NOT NULL DEFAULT true, test_status TEXT NOT NULL DEFAULT 'unknown',
		latency_ms INTEGER, outbound_ip TEXT, outbound_region TEXT, last_test_message TEXT,
		last_tested_at TIMESTAMPTZ,
		pool_health_status TEXT CHECK (pool_health_status IS NULL OR pool_health_status IN ('healthy','degraded','dead','unknown')),
		pool_health_failure_streak INTEGER NOT NULL DEFAULT 0,
		pool_health_checked_at TIMESTAMPTZ,
		created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL);
	CREATE UNIQUE INDEX idx_proxy_profiles_name_unique ON juhe_business.proxy_profiles(name);
	CREATE TABLE juhe_business.proxy_pools (
		id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT,
		strategy TEXT NOT NULL CHECK (strategy IN ('round_robin', 'weighted')),
		enabled BOOLEAN NOT NULL DEFAULT true,
		created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL);
	CREATE UNIQUE INDEX idx_proxy_pools_name_unique ON juhe_business.proxy_pools(name);
	CREATE TABLE juhe_business.proxy_pool_members (
		pool_id TEXT NOT NULL REFERENCES juhe_business.proxy_pools(id) ON DELETE RESTRICT,
		proxy_profile_id TEXT NOT NULL REFERENCES juhe_business.proxy_profiles(id) ON DELETE RESTRICT,
		priority INTEGER NOT NULL CHECK (priority >= 0),
		weight INTEGER NOT NULL DEFAULT 1 CHECK (weight BETWEEN 1 AND 100),
		enabled BOOLEAN NOT NULL DEFAULT true,
		created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (pool_id, proxy_profile_id),
		UNIQUE (pool_id, priority));
	CREATE TABLE juhe_business.accounts (
		id TEXT PRIMARY KEY, name TEXT NOT NULL, deleted_at TIMESTAMPTZ,
		proxy_pool_id TEXT REFERENCES juhe_business.proxy_pools(id) ON DELETE RESTRICT);
	CREATE TABLE juhe_business.account_proxy_leases (
		account_id TEXT PRIMARY KEY REFERENCES juhe_business.accounts(id) ON DELETE CASCADE,
		pool_id TEXT NOT NULL REFERENCES juhe_business.proxy_pools(id) ON DELETE RESTRICT,
		proxy_profile_id TEXT NOT NULL REFERENCES juhe_business.proxy_profiles(id) ON DELETE RESTRICT,
		generation BIGINT NOT NULL DEFAULT nextval('juhe_business.account_proxy_lease_generation_seq'),
		assignment_reason TEXT NOT NULL CHECK (assignment_reason IN ('initial', 'member_unavailable', 'member_removed', 'pool_reassigned')),
		assigned_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL,
		FOREIGN KEY (pool_id, proxy_profile_id) REFERENCES juhe_business.proxy_pool_members(pool_id, proxy_profile_id) ON DELETE RESTRICT);
`

type recordingInvalidator struct {
	calls []string // "topic|reason"
}

func (r *recordingInvalidator) Invalidate(topic, reason string) {
	r.calls = append(r.calls, topic+"|"+reason)
}

func (r *recordingInvalidator) reasons() []string {
	out := []string{}
	for _, call := range r.calls {
		out = append(out, strings.SplitN(call, "|", 2)[1])
	}
	return out
}

type recordingHealth struct {
	ids []string
}

func (r *recordingHealth) ResetMemberHealth(profileID string) {
	r.ids = append(r.ids, profileID)
}

type poolFixture struct {
	store  *Store
	db     *sql.DB
	sink   *recordingLogSink
	inval  *recordingInvalidator
	health *recordingHealth
	now    time.Time
}

type recordingLogSink struct {
	entries []authsys.OperationLogEntry
}

func (s *recordingLogSink) Record(entry authsys.OperationLogEntry, r *http.Request) {
	s.entries = append(s.entries, entry)
}

func newPoolFixture(t *testing.T) *poolFixture {
	t.Helper()
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, poolSchema)
		return err
	})
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	fixture := &poolFixture{
		db:     db,
		sink:   &recordingLogSink{},
		inval:  &recordingInvalidator{},
		health: &recordingHealth{},
		now:    now,
	}
	sequence := 0
	store, err := NewStore(Deps{
		DB:  db,
		Now: func() time.Time { return now },
		NewID: func(prefix string) string {
			sequence++
			return prefix + "_test_" + strconv.Itoa(sequence)
		},
		Inval:  fixture.inval,
		Health: fixture.health,
	})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	fixture.store = store
	return fixture
}

func (f *poolFixture) auth(role string) *authsys.AuthContext {
	if role == "" {
		role = "admin"
	}
	return &authsys.AuthContext{SystemAccountID: "sys-admin-1", Username: "admin", DisplayName: "Admin", Role: role, SessionID: "s1"}
}

func (f *poolFixture) seedProfile(t *testing.T, id, name string, enabled bool) {
	t.Helper()
	if _, err := f.db.Exec(`INSERT INTO juhe_business.proxy_profiles (id, system_account_id, name, type, host, port, enabled, test_status, created_at, updated_at)
		VALUES ($1, 'sa-1', $2, 'http', 'h.internal', 8080, $3, 'unknown', '2026-11-01', '2026-11-01')`, id, name, enabled); err != nil {
		t.Fatalf("seed profile %s: %v", id, err)
	}
}

func (f *poolFixture) seedPoolWithMembers(t *testing.T, poolID, poolName string, memberIDs []string) string {
	t.Helper()
	if _, err := f.db.Exec(`INSERT INTO juhe_business.proxy_pools (id, system_account_id, name, strategy, enabled, created_at, updated_at)
		VALUES ($1, 'sa-1', $2, 'round_robin', true, '2026-11-01T00:00:00.000000Z', '2026-11-01T00:00:00.000000Z')`, poolID, poolName); err != nil {
		t.Fatalf("seed pool: %v", err)
	}
	for index, memberID := range memberIDs {
		if _, err := f.db.Exec(`INSERT INTO juhe_business.proxy_pool_members (pool_id, proxy_profile_id, priority, weight, enabled, created_at, updated_at)
			VALUES ($1, $2, $3, 1, true, '2026-11-01', '2026-11-01')`, poolID, memberID, index); err != nil {
			t.Fatalf("seed member %s: %v", memberID, err)
		}
	}
	return "2026-11-01T00:00:00.000000Z"
}

func (f *poolFixture) seedLease(t *testing.T, accountID, poolID, profileID string) {
	t.Helper()
	if _, err := f.db.Exec(`INSERT INTO juhe_business.accounts (id,name) VALUES ($1,$1) ON CONFLICT (id) DO NOTHING`, accountID); err != nil {
		t.Fatalf("seed lease account: %v", err)
	}
	if _, err := f.db.Exec(`INSERT INTO juhe_business.account_proxy_leases (account_id, pool_id, proxy_profile_id, generation, assignment_reason, assigned_at, updated_at)
		VALUES ($1, $2, $3, 7, 'initial', '2026-11-02', '2026-11-02')`, accountID, poolID, profileID); err != nil {
		t.Fatalf("seed lease: %v", err)
	}
}

func (f *poolFixture) memberPriorities(t *testing.T, poolID string) map[string]int {
	t.Helper()
	rows, err := f.db.Query(`SELECT proxy_profile_id, priority FROM juhe_business.proxy_pool_members WHERE pool_id = $1 ORDER BY proxy_profile_id`, poolID)
	if err != nil {
		t.Fatalf("query members: %v", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var priority int
		if err := rows.Scan(&id, &priority); err != nil {
			t.Fatalf("scan member: %v", err)
		}
		out[id] = priority
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("members err: %v", err)
	}
	return out
}

func TestPoolCreateRouteNormalizesAndInvalidates(t *testing.T) {
	fixture := newPoolFixture(t)
	fixture.seedProfile(t, "p-1", "代理一", true)
	fixture.seedProfile(t, "p-2", "代理二", true)
	body := `{"name":"池一","description":"说明","strategy":"weighted","enabled":true,"members":[{"proxyProfileId":"p-2"},{"proxyProfileId":"p-1","weight":3}]}`
	recorder := httptest.NewRecorder()
	request := poolJSONRequest(t, http.MethodPost, "/__aisys__/api/proxy-pools", body)
	request = request.WithContext(authsys.WithAuthContext(request.Context(), fixture.auth("")))
	createHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create not 201: %d %s", recorder.Code, recorder.Body.String())
	}
	// priority 归一为连续 0..n-1（列表位置即顺序）。
	priorities := fixture.memberPriorities(t, "proxy_pool_test_1")
	if priorities["p-2"] != 0 || priorities["p-1"] != 1 {
		t.Fatalf("priorities wrong: %#v", priorities)
	}
	// 提交后失效与健康键语义：创建不删除健康键，只发 proxy_pool_updated。
	if len(fixture.inval.calls) != 1 || fixture.inval.calls[0] != "topic:gateway_runtime_cache|proxy_pool_updated" {
		t.Fatalf("invalidation wrong: %#v", fixture.inval.calls)
	}
	if len(fixture.health.ids) != 0 {
		t.Fatalf("create should not reset member health: %#v", fixture.health.ids)
	}
	if len(fixture.sink.entries) != 1 || fixture.sink.entries[0].Module != "proxy-pools" || fixture.sink.entries[0].Action != "create" {
		t.Fatalf("operation log wrong: %#v", fixture.sink.entries)
	}
	if strings.Contains(fixture.sink.entries[0].Summary, "host") {
		t.Fatalf("summary leaks host: %q", fixture.sink.entries[0].Summary)
	}
}

func poolJSONRequest(t *testing.T, method, target, body string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Length", strconv.Itoa(len(body)))
	// 测试直接调用 handler（不经 mux），显式补 {id} 路径参数。
	if id := strings.TrimPrefix(target, "/__aisys__/api/proxy-pools/"); id != target && id != "" {
		request.SetPathValue("id", id)
	}
	return request
}

func TestPoolCreateMemberValidationCodes(t *testing.T) {
	fixture := newPoolFixture(t)
	fixture.seedProfile(t, "p-1", "代理一", true)
	cases := []struct {
		name     string
		body     string
		wantCode int
		wantText string
	}{
		{"unknown member", `{"name":"池","strategy":"round_robin","members":[{"proxyProfileId":"missing"}]}`, 400, "proxy_pool_member_invalid"},
		{"duplicate member", `{"name":"池","strategy":"round_robin","members":[{"proxyProfileId":"p-1"},{"proxyProfileId":"p-1"}]}`, 400, "proxy_pool_member_duplicate"},
		{"weight range", `{"name":"池","strategy":"round_robin","members":[{"proxyProfileId":"p-1","weight":101}]}`, 400, "proxy_pool_member_invalid"},
	}
	for _, testCase := range cases {
		recorder := httptest.NewRecorder()
		request := poolJSONRequest(t, http.MethodPost, "/__aisys__/api/proxy-pools", testCase.body)
		request = request.WithContext(authsys.WithAuthContext(request.Context(), fixture.auth("")))
		createHandler(recorder, request, fixture.store, fixture.sink)
		if recorder.Code != testCase.wantCode || !strings.Contains(recorder.Body.String(), testCase.wantText) {
			t.Fatalf("%s: got %d %s", testCase.name, recorder.Code, recorder.Body.String())
		}
	}
	// >50 成员 → proxy_pool_member_limit。
	members := make([]string, 0, memberLimit+1)
	for index := 0; index <= memberLimit; index++ {
		members = append(members, `{"proxyProfileId":"p-`+itoa(index)+`"}`)
	}
	overflow := `{"name":"池","strategy":"round_robin","members":[` + strings.Join(members, ",") + `]}`
	recorder := httptest.NewRecorder()
	request := poolJSONRequest(t, http.MethodPost, "/__aisys__/api/proxy-pools", overflow)
	request = request.WithContext(authsys.WithAuthContext(request.Context(), fixture.auth("")))
	createHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != 400 || !strings.Contains(recorder.Body.String(), "proxy_pool_member_limit") {
		t.Fatalf("member limit wrong: %d %s", recorder.Code, recorder.Body.String())
	}
	// 重名 → 409。
	recorder = httptest.NewRecorder()
	request = poolJSONRequest(t, http.MethodPost, "/__aisys__/api/proxy-pools", `{"name":"池","strategy":"round_robin","members":[]}`)
	request = request.WithContext(authsys.WithAuthContext(request.Context(), fixture.auth("")))
	createHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("first create failed: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	request = poolJSONRequest(t, http.MethodPost, "/__aisys__/api/proxy-pools", `{"name":"池","strategy":"round_robin","members":[]}`)
	request = request.WithContext(authsys.WithAuthContext(request.Context(), fixture.auth("")))
	createHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "代理池名称已存在") {
		t.Fatalf("duplicate pool name wrong: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestPoolPatchTwoPhaseReplaceKeepsLeases(t *testing.T) {
	fixture := newPoolFixture(t)
	fixture.seedProfile(t, "p-a", "代理A", true)
	fixture.seedProfile(t, "p-b", "代理B", true)
	fixture.seedProfile(t, "p-c", "代理C", true)
	revision := fixture.seedPoolWithMembers(t, "pool-1", "池一", []string{"p-a", "p-b"})
	// 租约：p-a（保留成员，租约不动）、p-b（被移除成员，租约同事务删除）。
	fixture.seedLease(t, "acc-1", "pool-1", "p-a")
	fixture.seedLease(t, "acc-2", "pool-1", "p-b")
	body := `{"expectedUpdatedAt":"` + revision + `","members":[{"proxyProfileId":"p-c"},{"proxyProfileId":"p-a","weight":4}]}`
	recorder := httptest.NewRecorder()
	request := poolJSONRequest(t, http.MethodPatch, "/__aisys__/api/proxy-pools/pool-1", body)
	request = request.WithContext(authsys.WithAuthContext(request.Context(), fixture.auth("")))
	patchHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"changed":true`) {
		t.Fatalf("patch failed: %d %s", recorder.Code, recorder.Body.String())
	}
	// 两阶段后的最终 priority：p-c=0、p-a=1，p-b 行删除。
	priorities := fixture.memberPriorities(t, "pool-1")
	if priorities["p-c"] != 0 || priorities["p-a"] != 1 || len(priorities) != 2 {
		t.Fatalf("priorities after replace wrong: %#v", priorities)
	}
	var leaseCount int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM juhe_business.account_proxy_leases WHERE pool_id = 'pool-1'`).Scan(&leaseCount); err != nil {
		t.Fatalf("lease count: %v", err)
	}
	if leaseCount != 1 {
		t.Fatalf("kept lease lost or removed lease kept: %d", leaseCount)
	}
	var keptLease string
	if err := fixture.db.QueryRow(`SELECT account_id FROM juhe_business.account_proxy_leases WHERE pool_id = 'pool-1'`).Scan(&keptLease); err != nil || keptLease != "acc-1" {
		t.Fatalf("kept lease wrong: %q %v", keptLease, err)
	}
	// 被移除成员的健康键 best-effort 清理（§7.4）。
	if len(fixture.health.ids) != 1 || fixture.health.ids[0] != "p-b" {
		t.Fatalf("health reset wrong: %#v", fixture.health.ids)
	}
	if len(fixture.inval.reasons()) != 1 || fixture.inval.reasons()[0] != "proxy_pool_updated" {
		t.Fatalf("invalidation reasons wrong: %#v", fixture.inval.reasons())
	}
	// 操作日志记录成员变更摘要。
	if len(fixture.sink.entries) != 1 || fixture.sink.entries[0].Action != "update" || !strings.Contains(fixture.sink.entries[0].Summary, "池一") {
		t.Fatalf("operation log wrong: %#v", fixture.sink.entries)
	}
	membersChange := false
	for _, change := range fixture.sink.entries[0].Changes {
		if change.Field == "members" && strings.Contains(change.After, "移除 1") {
			membersChange = true
		}
	}
	if !membersChange {
		t.Fatalf("members change summary missing: %#v", fixture.sink.entries[0].Changes)
	}
}

func TestPoolPatchCASConflictAndNoop(t *testing.T) {
	fixture := newPoolFixture(t)
	fixture.seedProfile(t, "p-a", "代理A", true)
	revision := fixture.seedPoolWithMembers(t, "pool-1", "池一", []string{"p-a"})
	// 旧 revision → 409 proxy_pool_revision_conflict。
	recorder := httptest.NewRecorder()
	request := poolJSONRequest(t, http.MethodPatch, "/__aisys__/api/proxy-pools/pool-1",
		`{"expectedUpdatedAt":"2020-01-01T00:00:00.000000Z","name":"改名"}`)
	request = request.WithContext(authsys.WithAuthContext(request.Context(), fixture.auth("")))
	patchHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "proxy_pool_revision_conflict") {
		t.Fatalf("CAS conflict wrong: %d %s", recorder.Code, recorder.Body.String())
	}
	// 相同成员集合 + 相同字段 → changed:false，revision 不变。
	body := `{"expectedUpdatedAt":"` + revision + `","members":[{"proxyProfileId":"p-a"}]}`
	recorder = httptest.NewRecorder()
	request = poolJSONRequest(t, http.MethodPatch, "/__aisys__/api/proxy-pools/pool-1", body)
	request = request.WithContext(authsys.WithAuthContext(request.Context(), fixture.auth("")))
	patchHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"changed":false`) {
		t.Fatalf("noop patch wrong: %d %s", recorder.Code, recorder.Body.String())
	}
	if len(fixture.inval.calls) != 0 {
		t.Fatalf("noop patch should not invalidate: %#v", fixture.inval.calls)
	}
	var updatedAt string
	if err := fixture.db.QueryRow(`SELECT to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') FROM juhe_business.proxy_pools WHERE id = 'pool-1'`).Scan(&updatedAt); err != nil {
		t.Fatalf("read revision: %v", err)
	}
	if updatedAt != revision {
		t.Fatalf("noop patch bumped revision: %q", updatedAt)
	}
	// 纯重排也是变更：新 revision 可继续 PATCH。
	body = `{"expectedUpdatedAt":"` + revision + `","members":[{"proxyProfileId":"p-a","weight":2}]}`
	recorder = httptest.NewRecorder()
	request = poolJSONRequest(t, http.MethodPatch, "/__aisys__/api/proxy-pools/pool-1", body)
	request = request.WithContext(authsys.WithAuthContext(request.Context(), fixture.auth("")))
	patchHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"changed":true`) {
		t.Fatalf("weight change patch wrong: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestPoolDeleteBoundAccountsGuard(t *testing.T) {
	fixture := newPoolFixture(t)
	fixture.seedProfile(t, "p-a", "代理A", true)
	revision := fixture.seedPoolWithMembers(t, "pool-1", "池一", []string{"p-a"})
	fixture.seedLease(t, "acc-1", "pool-1", "p-a")
	// 在用账户 → 409 proxy_pool_bound_accounts（固定窗口预览账户名）。
	if _, err := fixture.db.Exec(`INSERT INTO juhe_business.accounts (id, name, proxy_pool_id) VALUES ('acc-1', '账户甲', 'pool-1')
		ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, proxy_pool_id = EXCLUDED.proxy_pool_id`); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	recorder := httptest.NewRecorder()
	request := poolJSONRequest(t, http.MethodDelete, "/__aisys__/api/proxy-pools/pool-1", "")
	request = request.WithContext(authsys.WithAuthContext(request.Context(), fixture.auth("")))
	deleteHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "proxy_pool_bound_accounts") {
		t.Fatalf("bound account guard wrong: %d %s", recorder.Code, recorder.Body.String())
	}
	if len(fixture.inval.calls) != 0 {
		t.Fatalf("rejected delete should not invalidate: %#v", fixture.inval.calls)
	}
	// 软删除账户的绑定不阻塞：同一事务置 NULL 并删除租约/成员/池（§4.5）。
	if _, err := fixture.db.Exec(`UPDATE juhe_business.accounts SET deleted_at = '2026-11-03' WHERE id = 'acc-1'`); err != nil {
		t.Fatalf("soft delete account: %v", err)
	}
	_ = revision
	recorder = httptest.NewRecorder()
	request = poolJSONRequest(t, http.MethodDelete, "/__aisys__/api/proxy-pools/pool-1", "")
	request = request.WithContext(authsys.WithAuthContext(request.Context(), fixture.auth("")))
	deleteHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete with soft-deleted account failed: %d %s", recorder.Code, recorder.Body.String())
	}
	var poolCount, memberCount, leaseCount, boundCount int
	if err := fixture.db.QueryRow(`SELECT
			(SELECT COUNT(*) FROM juhe_business.proxy_pools WHERE id = 'pool-1'),
			(SELECT COUNT(*) FROM juhe_business.proxy_pool_members WHERE pool_id = 'pool-1'),
			(SELECT COUNT(*) FROM juhe_business.account_proxy_leases WHERE pool_id = 'pool-1'),
			(SELECT COUNT(*) FROM juhe_business.accounts WHERE id = 'acc-1' AND proxy_pool_id IS NOT NULL)`).
		Scan(&poolCount, &memberCount, &leaseCount, &boundCount); err != nil {
		t.Fatalf("verify delete: %v", err)
	}
	if poolCount != 0 || memberCount != 0 || leaseCount != 0 || boundCount != 0 {
		t.Fatalf("delete residue: pools=%d members=%d leases=%d bound=%d", poolCount, memberCount, leaseCount, boundCount)
	}
	if len(fixture.health.ids) != 1 || fixture.health.ids[0] != "p-a" {
		t.Fatalf("pool delete health reset wrong: %#v", fixture.health.ids)
	}
	if len(fixture.inval.reasons()) != 1 || fixture.inval.reasons()[0] != "proxy_pool_updated" {
		t.Fatalf("pool delete invalidation wrong: %#v", fixture.inval.reasons())
	}
	if len(fixture.sink.entries) != 1 || fixture.sink.entries[0].Module != "proxy-pools" || fixture.sink.entries[0].Action != "delete" {
		t.Fatalf("delete operation log wrong: %#v", fixture.sink.entries)
	}
}

func TestPoolDetailAssignableProjection(t *testing.T) {
	fixture := newPoolFixture(t)
	fixture.seedProfile(t, "p-a", "代理A", true)
	fixture.seedProfile(t, "p-b", "代理B", false) // 代理停用 → 不可分配
	fixture.seedProfile(t, "p-c", "代理C", true)
	fixture.seedProfile(t, "p-d", "代理D", true)
	if _, err := fixture.db.Exec(`UPDATE juhe_business.proxy_profiles SET pool_health_status = 'dead', pool_health_failure_streak = 3 WHERE id = 'p-c'`); err != nil {
		t.Fatalf("mark dead: %v", err)
	}
	fixture.seedPoolWithMembers(t, "pool-1", "池一", []string{"p-a", "p-b", "p-c", "p-d"})
	fixture.seedLease(t, "acc-1", "pool-1", "p-a")
	detail, err := fixture.store.GetDetail(context.Background(), "pool-1")
	if err != nil || detail == nil {
		t.Fatalf("detail: %v %#v", err, detail)
	}
	if detail.MemberCount != 4 || detail.LeaseCount != 1 || detail.AssignableMemberCount != 2 {
		t.Fatalf("counts wrong: member=%d assignable=%d lease=%d", detail.MemberCount, detail.AssignableMemberCount, detail.LeaseCount)
	}
	assignable := map[string]bool{}
	for _, member := range detail.Members {
		assignable[member.ProxyProfileID] = member.Assignable
		if member.ProxyProfileID == "p-c" && (member.PoolHealthStatus != "dead" || member.PoolHealthFailureStreak != 3) {
			t.Fatalf("dead member projection wrong: %#v", member)
		}
		if member.ProxyProfileID == "p-d" && member.PoolHealthStatus != "unknown" {
			t.Fatalf("NULL health should project unknown: %#v", member)
		}
	}
	if !assignable["p-a"] || assignable["p-b"] || assignable["p-c"] || !assignable["p-d"] {
		t.Fatalf("assignable wrong: %#v", assignable)
	}
	// 成员投影不泄露 host/凭据。
	encoded, _ := json.Marshal(detail)
	for _, forbidden := range []string{"host", "password", "username", "outboundIp", "h.internal"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("detail leaks %s: %s", forbidden, string(encoded))
		}
	}
}

func TestPoolListPageKeywordAndCounts(t *testing.T) {
	fixture := newPoolFixture(t)
	fixture.seedProfile(t, "p-a", "代理A", true)
	fixture.seedProfile(t, "p-b", "代理B", true)
	fixture.seedPoolWithMembers(t, "pool-1", "出口池甲", []string{"p-a"})
	fixture.seedPoolWithMembers(t, "pool-2", "出口池乙", []string{"p-a", "p-b"})
	fixture.seedLease(t, "acc-1", "pool-2", "p-a")
	result, err := fixture.store.ListPage(context.Background(), 1, 20, "出口池")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if result.Total != 2 || len(result.Items) != 2 {
		t.Fatalf("keyword window wrong: %#v", result)
	}
	// updated_at DESC：乙池成员更多，seed 时间相同则按 id DESC。
	result, err = fixture.store.ListPage(context.Background(), 1, 20, "出口池乙")
	if err != nil || result.Total != 1 || result.Items[0].MemberCount != 2 || result.Items[0].LeaseCount != 1 || result.Items[0].AssignableMemberCount != 2 {
		t.Fatalf("filtered list wrong: %#v %v", result, err)
	}
	result, err = fixture.store.ListPage(context.Background(), 1, 20, "不存在")
	if err != nil || result.Total != 0 || len(result.Items) != 0 {
		t.Fatalf("empty keyword wrong: %#v %v", result, err)
	}
}

func TestPoolMemberOptionsViaStore(t *testing.T) {
	fixture := newPoolFixture(t)
	fixture.seedProfile(t, "p-1", "Beta", true)
	fixture.seedProfile(t, "p-2", "Alpha", false)
	if _, err := fixture.db.Exec(`UPDATE juhe_business.proxy_profiles SET test_status = 'failed' WHERE id = 'p-2'`); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	// member-options 不过滤 enabled（选择器展示 enabled 字段），不返回
	// host/凭据；selectedIds 并入。
	options, err := fixture.store.ListMemberOptions(context.Background(), "", 50, []string{"p-1"})
	if err != nil {
		t.Fatalf("member options: %v", err)
	}
	if len(options) != 2 || options[0].ID != "p-2" || options[0].Enabled || options[0].TestStatus != "failed" || options[1].ID != "p-1" {
		t.Fatalf("member options wrong: %#v", options)
	}
	encoded, _ := json.Marshal(options)
	for _, forbidden := range []string{"host", "port", "password", "username", "h.internal"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("member options leak %s: %s", forbidden, string(encoded))
		}
	}
}
