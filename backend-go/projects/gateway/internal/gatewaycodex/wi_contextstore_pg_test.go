package gatewaycodex

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/sqlpool"
)

func TestWIJSONSmallHelpers(t *testing.T) {
	// mapHasKey：nil record 安全 + 命中/未命中（值为 nil 的键也算存在）。
	if mapHasKey(nil, "k") {
		t.Fatal("nil record 不得命中")
	}
	record := jsonRecord{"k": nil}
	if !mapHasKey(record, "k") {
		t.Fatal("键存在（值 nil）必须命中")
	}
	if mapHasKey(record, "missing") {
		t.Fatal("缺失键不得命中")
	}
	// inputOf：nil record → nil。
	if inputOf(nil) != nil {
		t.Fatal("nil record 必须返回 nil")
	}
	if got := inputOf(jsonRecord{"input": 7}); got != 7 {
		t.Fatalf("inputOf=%v", got)
	}
	// orElse / numberAsInt64。
	if got := orElse("", "fallback"); got != "fallback" {
		t.Fatalf("orElse=%q", got)
	}
	if got := orElse("v", "fallback"); got != "v" {
		t.Fatalf("orElse=%q", got)
	}
	if got := numberAsInt64(2.9); got != 2 {
		t.Fatalf("float64→%d", got)
	}
	if got := numberAsInt64(int64(5)); got != 5 {
		t.Fatalf("int64→%d", got)
	}
	if got := numberAsInt64(3); got != 3 {
		t.Fatalf("int→%d", got)
	}
	if got := numberAsInt64("x"); got != 0 {
		t.Fatalf("其他类型→%d", got)
	}
	// jsonRefEqual 的引用 vs 值语义。
	if !jsonRefEqual(nil, nil) {
		t.Fatal("nil==nil")
	}
	if jsonRefEqual(nil, 1) || jsonRefEqual(1, nil) {
		t.Fatal("nil 与标量不相等")
	}
	a := jsonRecord{"x": 1}
	if !jsonRefEqual(a, a) {
		t.Fatal("同一 map 引用必须相等")
	}
	if jsonRefEqual(a, jsonRecord{"x": 1}) {
		t.Fatal("不同 map 引用不相等")
	}
	slice := []any{1}
	if !jsonRefEqual(slice, slice) {
		t.Fatal("同一 slice 引用必须相等")
	}
	if jsonRefEqual([]any{1}, []any{1}) {
		t.Fatal("不同 slice 引用不相等")
	}
	if !jsonRefEqual("s", "s") {
		t.Fatal("标量按值比较")
	}
	if jsonRefEqual("s", jsonRecord{}) {
		t.Fatal("标量与 map 不相等")
	}
	if !jsonRefNotEqual(a, jsonRecord{"x": 1}) {
		t.Fatal("引用不等必须成立")
	}
	if got := derefString(nil); got != "" {
		t.Fatalf("derefString(nil)=%q", got)
	}
	if got := derefString(optionalJSONString("v")); got != "v" {
		t.Fatalf("derefString=%q", got)
	}
}

func TestWICompactionReferenceParsing(t *testing.T) {
	digest := strings.Repeat("a", 64)
	// 合法引用。
	ref, ok := parseCodexCompactionReference(codexCompactionReferencePrefix + "id-1." + digest)
	if !ok || ref.compactID != "id-1" || ref.digest != digest {
		t.Fatalf("ref=%+v ok=%v", ref, ok)
	}
	// 大写 digest 归一化小写。
	ref, ok = parseCodexCompactionReference(codexCompactionReferencePrefix + "id-1." + strings.ToUpper(digest))
	if !ok || ref.digest != digest {
		t.Fatalf("大写 digest 归一化失败: %+v", ref)
	}
	invalid := []string{
		"",
		codexCompactionReferencePrefix,
		codexCompactionReferencePrefix + ".abcdef",
		codexCompactionReferencePrefix + "id.",
		codexCompactionReferencePrefix + "id." + strings.Repeat("g", 64),
		codexCompactionReferencePrefix + "id." + strings.Repeat("a", 63),
	}
	for i, value := range invalid {
		if _, ok := parseCodexCompactionReference(value); ok {
			t.Fatalf("第 %d 个非法引用不得解析: %q", i, value)
		}
	}
	// isSHA256Hex 边界。
	if !isSHA256Hex(strings.Repeat("F", 64)) {
		t.Fatal("大写 hex 必须合法")
	}
	if isSHA256Hex("") || isSHA256Hex(strings.Repeat("a", 65)) {
		t.Fatal("长度必须为 64")
	}
}

func TestWIStateRestoreFailureCopy(t *testing.T) {
	// 四种恢复失败的 HTTP 语义。
	cases := []struct {
		outcome string
		status  int
		code    string
	}{
		{RestoreOutcomeBoundaryMismatch, 403, "codex_bridge_previous_response_boundary_mismatch"},
		{RestoreOutcomeChainTooDeep, 413, "codex_bridge_previous_response_chain_too_deep"},
		{RestoreOutcomeChainBroken, 404, "codex_bridge_previous_response_chain_broken"},
		{"unknown", 404, "codex_bridge_previous_response_not_found"},
	}
	for _, tc := range cases {
		failure := stateRestoreFailure(tc.outcome)
		if failure.statusCode != tc.status || failure.code != tc.code {
			t.Fatalf("outcome=%q failure=%+v", tc.outcome, failure)
		}
	}
	if failure := compactReferenceFailure(RestoreOutcomeBoundaryMismatch); failure.statusCode != 403 {
		t.Fatalf("compact 边界失败=%+v", failure)
	}
	if failure := compactReferenceFailure("other"); failure.statusCode != 404 || failure.code != "codex_bridge_compact_snapshot_not_found" {
		t.Fatalf("compact 默认失败=%+v", failure)
	}
}

func TestWIContextRequestStateRegistryRelease(t *testing.T) {
	registry := NewContextRequestStateRegistry()
	req := newTestRequest(t, "POST", "/v1/responses", []byte(`{"input":[]}`), nil)
	if _, ok := registry.Get(req); ok {
		t.Fatal("空 registry 不得命中")
	}
	state := &CodexResponsesContextRequestState{}
	registry.Set(req, state)
	got, ok := registry.Get(req)
	if !ok || got != state {
		t.Fatalf("Get=%+v ok=%v", got, ok)
	}
	// Release 移除条目；重复释放安全。
	registry.Release(req)
	if _, ok := registry.Get(req); ok {
		t.Fatal("Release 后不得命中")
	}
	registry.Release(req)
	// nil 防护。
	if _, ok := registry.Get(nil); ok {
		t.Fatal("nil req 不得命中")
	}
	registry.Set(nil, state)
	var nilRegistry *ContextRequestStateRegistry
	nilRegistry.Release(req)
}

func TestWICompactSummaryDispatcherFunc(t *testing.T) {
	// 函数适配器必须把调用与载荷原样转发。
	called := false
	var got CompactSummaryDispatchInput
	dispatcher := CompactSummaryDispatcherFunc(func(_ context.Context, input CompactSummaryDispatchInput) (*CompactUpstreamExchange, error) {
		called = true
		got = input
		return nil, nil
	})
	input := CompactSummaryDispatchInput{StartedAt: 42}
	if _, err := dispatcher.DispatchCompactSummary(context.Background(), input); err != nil {
		t.Fatalf("dispatch 失败: %v", err)
	}
	if !called || got.StartedAt != 42 {
		t.Fatalf("载荷未透传: called=%v got=%+v", called, got)
	}
}

func TestWIBridgeCompactRestoresInternalPrevious(t *testing.T) {
	service := &CompactPreflightService{Registry: NewContextRequestStateRegistry()}
	req := newTestRequest(t, "POST", "/v1/responses/compact", []byte(`{}`), nil)
	// 无注册状态 → false。
	if service.bridgeCompactRestoresInternalPrevious(CompactPreflightInput{Req: req}) {
		t.Fatal("无注册状态不得恢复内部链")
	}
	// internal previous → true。
	service.Registry.Set(req, &CodexResponsesContextRequestState{PreviousResponseKind: PreviousKindInternal})
	if !service.bridgeCompactRestoresInternalPrevious(CompactPreflightInput{Req: req}) {
		t.Fatal("internal previous 必须恢复内部链")
	}
	// 其他 kind → false。
	service.Registry.Set(req, &CodexResponsesContextRequestState{PreviousResponseKind: PreviousKindExternal})
	if service.bridgeCompactRestoresInternalPrevious(CompactPreflightInput{Req: req}) {
		t.Fatal("upstream previous 不得恢复内部链")
	}
}

func TestWICodexContextTablePostgresHelper(t *testing.T) {
	if got := codexContextTablePostgres("codex_context_sessions"); got != "juhe_codex_context.codex_context_sessions" {
		t.Fatalf("table=%q", got)
	}
	if got := postgresPlaceholders(3); got != "$1, $2, $3" {
		t.Fatalf("placeholders=%q", got)
	}
	if got := postgresPlaceholders(0); got != "" {
		t.Fatalf("空占位符=%q", got)
	}
}

// wiOpenSQLiteHandle 构造一个挂在 sqlpool 注册表上的 SQLite 句柄。
func wiOpenSQLiteHandle(t *testing.T, dsn string) *sqlpool.Handle {
	t.Helper()
	registry := sqlpool.NewRegistry()
	handle, err := registry.Acquire(
		func() (*sql.DB, error) { return sql.Open("sqlite", dsn) },
		"file:"+dsn, "test", 1, 1,
	)
	if err != nil {
		t.Fatalf("sqlpool Acquire 失败: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	return handle
}

func TestWIPostgresContextStateStoreOverSQLite(t *testing.T) {
	// 契约：postgres driver 的 SQL 限定 juhe_codex_context schema；用 SQLite
	// 承载时 schema 不存在，所有写读必须报错（错误透传，不静默降级）。
	if _, err := NewPostgresContextStateStore(nil); err == nil {
		t.Fatal("nil handle 必须报错")
	}
	handle := wiOpenSQLiteHandle(t, "wi-codex-pg?mode=memory&cache=shared")
	store, err := NewPostgresContextStateStore(handle)
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	ctx := context.Background()

	// 读路径：schema 缺失 → 查询错误。
	if _, err := store.ReadResponseStateRow(ctx, "resp-1"); err == nil {
		t.Fatal("postgres 读必须报错（schema 缺失）")
	}
	if _, err := store.ReadCompactStateRow(ctx, "compact-1"); err == nil {
		t.Fatal("postgres compact 读必须报错")
	}
	// 写路径：事务打开成功、upsert 失败 → 错误并回滚。
	responseRow := CodexContextResponseStateIndex{
		ResponseID: "resp-1",
		SessionID:  "s1",
		UpdatedAt:  "2026-01-01T00:00:00.000Z",
		ExpiresAt:  "2026-01-02T00:00:00.000Z",
	}
	if err := store.SaveResponseStateRow(ctx, responseRow); err == nil {
		t.Fatal("postgres 写必须报错")
	}
	compactRow := CodexContextCompactStateIndex{
		CompactID: "compact-1",
		SessionID: "s1",
		UpdatedAt: "2026-01-01T00:00:00.000Z",
		ExpiresAt: "2026-01-02T00:00:00.000Z",
	}
	if err := store.SaveCompactStateRow(ctx, compactRow); err == nil {
		t.Fatal("postgres compact 写必须报错")
	}
	// touch：空 rows 是合法 no-op。
	if err := store.TouchResponseChain(ctx, nil, "now", "later"); err != nil {
		t.Fatalf("空 rows 必须无错: %v", err)
	}
	if err := store.TouchResponseChain(ctx, []CodexContextResponseStateIndex{responseRow}, "now", "later"); err == nil {
		t.Fatal("postgres touch 必须报错")
	}
	if err := store.TouchCompact(ctx, compactRow, "now", "later"); err == nil {
		t.Fatal("postgres compact touch 必须报错")
	}
	// upsert helper 直接执行同样报错（juhe_codex_context 表不存在）。
	if db := handle.DB(); db != nil {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("打开事务失败: %v", err)
		}
		if err := upsertSessionRowPostgres(ctx, tx, sessionUpsertInput{sessionID: "s1"}); err == nil {
			t.Fatal("postgres upsert 必须报错")
		}
		_ = tx.Rollback()
	}
}

// ---------------------------------------------------------------------------
// PostgresContextStateStore 臂正测试（组A，pgkit_test.go 夹具）：与分片臂既有
// 矩阵（contextstore_test.go / wi_shard_store_test.go，组B）断言逐字节一致。

// TestWIPostgresContextStateStoreRowContract 镜像 TestWIShardStoreCRUDRoundTrip
// 的行契约（分片特有的 Close 后文件重开尾部不适用 PG 臂，不迁移）。
func TestWIPostgresContextStateStoreRowContract(t *testing.T) {
	store := newPostgresContextStateStore(t)
	ctx := context.Background()

	row := CodexContextResponseStateIndex{
		CodexContextStateBoundary: CodexContextStateBoundary{
			SystemAccountID: "sys", GroupID: "group",
		},
		ResponseID:         "resp-1",
		SessionID:          "session-1",
		PreviousResponseID: "",
		Model:              "gpt-5",
		UpstreamModel:      "gpt-5",
		CreatedAt:          "2026-01-01T00:00:00.000Z",
		UpdatedAt:          "2026-01-01T00:00:00.000Z",
		ExpiresAt:          "2026-01-02T00:00:00.000Z",
	}
	if err := store.SaveResponseStateRow(ctx, row); err != nil {
		t.Fatalf("保存响应行失败: %v", err)
	}
	loaded, err := store.ReadResponseStateRow(ctx, "resp-1")
	if err != nil {
		t.Fatalf("读取响应行失败: %v", err)
	}
	if loaded.ResponseID != "resp-1" || loaded.SessionID != "session-1" || loaded.Model != "gpt-5" {
		t.Fatalf("读回=%+v", loaded)
	}
	compact := CodexContextCompactStateIndex{
		CodexContextStateBoundary: row.CodexContextStateBoundary,
		CompactID:                 "compact-1",
		SessionID:                 "session-1",
		SummaryDigest:             stringsRepeat("a", 64),
		UpdatedAt:                 "2026-01-01T00:00:00.000Z",
		ExpiresAt:                 "2026-01-02T00:00:00.000Z",
	}
	if err := store.SaveCompactStateRow(ctx, compact); err != nil {
		t.Fatalf("保存 compact 行失败: %v", err)
	}
	loadedCompact, err := store.ReadCompactStateRow(ctx, "compact-1")
	if err != nil || loadedCompact.CompactID != "compact-1" || loadedCompact.SummaryDigest != stringsRepeat("a", 64) {
		t.Fatalf("读回 compact=%+v err=%v", loadedCompact, err)
	}
	if err := store.TouchResponseChain(ctx, []CodexContextResponseStateIndex{row}, "2026-01-03T00:00:00.000Z", "2026-01-04T00:00:00.000Z"); err != nil {
		t.Fatalf("touch 链失败: %v", err)
	}
	refreshed, err := store.ReadResponseStateRow(ctx, "resp-1")
	if err != nil || refreshed.LastUsedAt != "2026-01-03T00:00:00.000Z" || refreshed.ExpiresAt != "2026-01-04T00:00:00.000Z" {
		t.Fatalf("touch 后=%+v err=%v", refreshed, err)
	}
	if err := store.TouchCompact(ctx, compact, "2026-01-03T00:00:00.000Z", "2026-01-05T00:00:00.000Z"); err != nil {
		t.Fatalf("touch compact 失败: %v", err)
	}
	refreshedCompact, err := store.ReadCompactStateRow(ctx, "compact-1")
	if err != nil || refreshedCompact.ExpiresAt != "2026-01-05T00:00:00.000Z" {
		t.Fatalf("compact touch 后=%+v err=%v", refreshedCompact, err)
	}
	if err := store.TouchResponseChain(ctx, nil, "now", "later"); err != nil {
		t.Fatalf("空链 touch=%v", err)
	}
	if loadedMissing, err := store.ReadResponseStateRow(ctx, "missing"); err != nil || loadedMissing != nil {
		t.Fatalf("缺失行=%+v err=%v", loadedMissing, err)
	}
	if loadedMissingCompact, err := store.ReadCompactStateRow(ctx, "missing"); err != nil || loadedMissingCompact != nil {
		t.Fatalf("缺失 compact=%+v err=%v", loadedMissingCompact, err)
	}
}

// TestWIPostgresContextStateStoreChainOutcomes 镜像
// TestReadCodexContextResponseStateChainOutcomes 的链读矩阵（每子测独立库，
// 与分片臂同构，行不得跨子测累积）。
func TestWIPostgresContextStateStoreChainOutcomes(t *testing.T) {
	ctx := context.Background()
	base := CodexContextStateBoundary{SystemAccountID: "sys", GroupID: "group"}
	makeRow := func(id, previousID string, expiresAt string) CodexContextResponseStateIndex {
		row := CodexContextResponseStateIndex{
			CodexContextStateBoundary: base,
			CodexContextPayloadReference: CodexContextPayloadReference{
				StorageKey: "k", SHA256: "h", Compression: "gzip", SchemaVersion: 2,
				RawSizeBytes: 1, CompressedSizeBytes: 1,
			},
			ResponseID: id,
			SessionID:  "session-a",
			CreatedAt:  "2026-09-04T00:00:00.000Z",
			UpdatedAt:  "2026-09-04T00:00:00.000Z",
			LastUsedAt: "2026-09-04T00:00:00.000Z",
			ExpiresAt:  expiresAt,
		}
		if previousID != "" {
			row.PreviousResponseID = previousID
		}
		return row
	}
	now := "2026-09-05T00:00:00.000Z"
	fresh := "2026-09-20T00:00:00.000Z"

	tests := []struct {
		name        string
		rows        []CodexContextResponseStateIndex
		startAt     string
		maxDepth    int
		wantOutcome string
		wantCount   int
		wantFirstID string
	}{
		{
			name:        "not found",
			startAt:     "resp_missing",
			wantOutcome: CodexContextOutcomeNotFound,
		},
		{
			name:        "single row found",
			rows:        []CodexContextResponseStateIndex{makeRow("resp_a", "", fresh)},
			startAt:     "resp_a",
			wantOutcome: CodexContextOutcomeFound,
			wantCount:   1,
			wantFirstID: "resp_a",
		},
		{
			name: "chain found reversed",
			rows: []CodexContextResponseStateIndex{
				makeRow("resp_c", "resp_b", fresh),
				makeRow("resp_b", "resp_a", fresh),
				makeRow("resp_a", "", fresh),
			},
			startAt:     "resp_c",
			wantOutcome: CodexContextOutcomeFound,
			wantCount:   3,
			wantFirstID: "resp_a",
		},
		{
			name:        "chain broken",
			rows:        []CodexContextResponseStateIndex{makeRow("resp_c", "resp_b", fresh)},
			startAt:     "resp_c",
			wantOutcome: CodexContextOutcomeChainBroken,
		},
		{
			name:        "expired",
			rows:        []CodexContextResponseStateIndex{makeRow("resp_e", "", "2026-09-04T12:00:00.000Z")},
			startAt:     "resp_e",
			wantOutcome: CodexContextOutcomeExpired,
		},
		{
			name: "boundary mismatch",
			rows: []CodexContextResponseStateIndex{func() CodexContextResponseStateIndex {
				row := makeRow("resp_x", "", fresh)
				row.APIKeyID = "other-key"
				return row
			}()},
			startAt:     "resp_x",
			wantOutcome: CodexContextOutcomeBoundaryMismat,
		},
		{
			name: "chain too deep",
			rows: []CodexContextResponseStateIndex{
				makeRow("resp_deep_a", "resp_deep_b", fresh),
				makeRow("resp_deep_b", "", fresh),
			},
			startAt:     "resp_deep_a",
			maxDepth:    1,
			wantOutcome: CodexContextOutcomeChainTooDeep,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newPostgresContextStateStore(t)
			for _, row := range tt.rows {
				if err := SaveCodexContextResponseStateIndex(ctx, store, row); err != nil {
					t.Fatalf("save %s: %v", row.ResponseID, err)
				}
			}
			result, err := ReadCodexContextResponseStateChain(ctx, store, ResponseChainReadInput{
				ResponseID: tt.startAt,
				Boundary:   base,
				MaxDepth:   tt.maxDepth,
				Now:        now,
			})
			if err != nil {
				t.Fatalf("read chain: %v", err)
			}
			if result.Outcome != tt.wantOutcome {
				t.Fatalf("outcome = %q, want %q", result.Outcome, tt.wantOutcome)
			}
			if tt.wantOutcome == CodexContextOutcomeFound {
				if len(result.Responses) != tt.wantCount {
					t.Fatalf("responses = %d, want %d", len(result.Responses), tt.wantCount)
				}
				if result.Responses[0].ResponseID != tt.wantFirstID {
					t.Errorf("first = %q, want %q", result.Responses[0].ResponseID, tt.wantFirstID)
				}
				if result.SessionID != "session-a" {
					t.Errorf("sessionId = %q", result.SessionID)
				}
				touched, err := store.ReadResponseStateRow(ctx, tt.startAt)
				if err != nil {
					t.Fatalf("read touched: %v", err)
				}
				if touched.ExpiresAt != "2026-09-05T00:00:00.000Z" {
					t.Errorf("touch did not refresh expiresAt: %q", touched.ExpiresAt)
				}
			}
		})
	}
}

// TestWIPostgresContextStateStoreCompactOutcomes 镜像
// TestReadCodexContextCompactStateOutcomes 的 compact 读矩阵。
func TestWIPostgresContextStateStoreCompactOutcomes(t *testing.T) {
	store := newPostgresContextStateStore(t)
	ctx := context.Background()
	base := CodexContextStateBoundary{SystemAccountID: "sys", GroupID: "group"}
	row := CodexContextCompactStateIndex{
		CodexContextStateBoundary: base,
		CodexContextPayloadReference: CodexContextPayloadReference{
			StorageKey: "k", SHA256: "h", Compression: "gzip", SchemaVersion: 2,
			RawSizeBytes: 1, CompressedSizeBytes: 1,
		},
		CompactID:     "cmp_1",
		SessionID:     "session-c",
		SummaryDigest: stringsRepeat("a", 64),
		CreatedAt:     "2026-09-04T00:00:00.000Z",
		UpdatedAt:     "2026-09-04T00:00:00.000Z",
		LastUsedAt:    "2026-09-04T00:00:00.000Z",
		ExpiresAt:     "2026-09-20T00:00:00.000Z",
	}
	if err := SaveCodexContextCompactStateIndex(ctx, store, row); err != nil {
		t.Fatalf("save: %v", err)
	}
	found, err := ReadCodexContextCompactState(ctx, store, CompactStateReadInput{CompactID: "cmp_1", Boundary: base, Now: "2026-09-05T00:00:00.000Z"})
	if err != nil || found.Outcome != CodexContextOutcomeFound || found.Compact == nil {
		t.Fatalf("found = %+v err %v", found, err)
	}
	if found.Compact.SessionID != "session-c" {
		t.Errorf("sessionId = %q", found.Compact.SessionID)
	}
	notFound, err := ReadCodexContextCompactState(ctx, store, CompactStateReadInput{CompactID: "cmp_missing", Boundary: base, Now: "2026-09-05T00:00:00.000Z"})
	if err != nil || notFound.Outcome != CodexContextOutcomeNotFound {
		t.Errorf("notFound = %+v err %v", notFound, err)
	}
	// provider_code 维度已退役（B4①），边界失配改由 apiKey 维度验证。
	otherBoundary := base
	otherBoundary.APIKeyID = "key-other"
	mismatch, err := ReadCodexContextCompactState(ctx, store, CompactStateReadInput{CompactID: "cmp_1", Boundary: otherBoundary, Now: "2026-09-05T00:00:00.000Z"})
	if err != nil || mismatch.Outcome != CodexContextOutcomeBoundaryMismat {
		t.Errorf("mismatch = %+v err %v", mismatch, err)
	}
}
