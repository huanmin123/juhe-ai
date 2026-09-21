package modelcheckowner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
	_ "modernc.org/sqlite"
)

// 游标比较契约：时间优先、ID 决胜，畸形时间必须给出确定性的排序。
func TestWBCompareTrustCursorTable(t *testing.T) {
	cases := []struct {
		name  string
		left  [2]string
		right [2]string
		want  int
	}{
		{name: "empty both", left: [2]string{"", "b"}, right: [2]string{"", "a"}, want: 1},
		{name: "left empty", left: [2]string{"", "b"}, right: [2]string{"2026-09-01T00:00:00Z", "a"}, want: -1},
		{name: "right empty", left: [2]string{"2026-09-01T00:00:00Z", "a"}, right: [2]string{"", "b"}, want: 1},
		{name: "earlier time", left: [2]string{"2026-09-01T00:00:00Z", "z"}, right: [2]string{"2026-09-02T00:00:00Z", "a"}, want: -1},
		{name: "same instant different zone", left: [2]string{"2026-09-01T01:00:00+01:00", "a"}, right: [2]string{"2026-09-01T00:00:00Z", "b"}, want: -1},
		{name: "same time id decides", left: [2]string{"2026-09-01T00:00:00Z", "a"}, right: [2]string{"2026-09-01T00:00:00Z", "b"}, want: -1},
		{name: "left malformed", left: [2]string{"junk", "a"}, right: [2]string{"2026-09-01T00:00:00Z", "b"}, want: -1},
		{name: "right malformed", left: [2]string{"2026-09-01T00:00:00Z", "a"}, right: [2]string{"junk", "b"}, want: 1},
		{name: "both malformed", left: [2]string{"junk", "a"}, right: [2]string{"junk", "b"}, want: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := compareTrustCursor(tc.left[0], tc.left[1], tc.right[0], tc.right[1])
			if got != tc.want {
				t.Fatalf("compareTrustCursor=%d want=%d", got, tc.want)
			}
		})
	}
}

// 信任聚合辅助契约：映射状态冲突降级 unknown，协议状态 failed 优先。
func TestWBTrustAggregationHelpers(t *testing.T) {
	if got := compactTrustReasons([]string{" a ", "", "a", "b", "b"}); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("compactTrustReasons=%v", got)
	}
	if got := trustMappingStatus(nil); got != "unknown" {
		t.Fatalf("空观测映射状态=%q", got)
	}
	mixed := []trustObservation{{mappingStatus: "direct"}, {mappingStatus: "unknown"}}
	if got := trustMappingStatus(mixed); got != "mixed" {
		t.Fatalf("混合映射状态=%q", got)
	}
	blank := []trustObservation{{mappingStatus: " "}}
	if got := trustMappingStatus(blank); got != "unknown" {
		t.Fatalf("空白映射状态=%q", got)
	}
	if got := trustProtocolStatus(nil); got != "insufficient_evidence" {
		t.Fatalf("空观测协议状态=%q", got)
	}
	if got := trustProtocolStatus([]trustObservation{{protocolStatus: "failed"}}); got != "failed" {
		t.Fatalf("失败协议状态=%q", got)
	}
	if got := trustProtocolStatus([]trustObservation{{protocolStatus: "consistent"}, {protocolStatus: "warning"}}); got != "warning" {
		t.Fatalf("警告协议状态=%q", got)
	}
	if got := trustProtocolStatus([]trustObservation{{protocolStatus: "mystery"}}); got != "insufficient_evidence" {
		t.Fatalf("未知协议状态=%q", got)
	}
}

func wbValidTrustReport() TrustReport {
	return TrustReport{IdentityStatus: "consistent", MappingStatus: "direct", UsageIntegrityStatus: "consistent", ProtocolStatus: "consistent", EvidenceStatus: "stable", EvidenceCoverage: 80, TrustScore: 0.9, TrustFormed: true}
}

// ProjectTrust 契约：收据去重、游标单调推进、最新读模型只在更新时改写；
// 同游标不同事实的重放必须失败关闭。
func TestWBStoreProjectTrustLifecycle(t *testing.T) {
	store := &Store{db: wbOpenMemoryDB(t, runtimeTestDDL()), mode: "sqlite"}
	defer store.Close()
	ctx := context.Background()
	seedObservation := func(id, runID, createdAt string) {
		t.Helper()
		if _, err := store.db.Exec(`INSERT INTO model_check_observations (id,run_id,system_account_id,account_id,provider_code,requested_model,mapped_upstream_model,probe_family,observation_status,identity_status,mapping_status,protocol_status,evidence_coverage,created_at) VALUES (?,'run-trust','sys','acct','openai','gpt-5.6-sol','gpt-5.6-sol','stability','complete','consistent','direct','consistent',80,?)`, id, createdAt); err != nil {
			t.Fatal(err)
		}
	}
	projection := func(runID string, report TrustReport) TrustProjection {
		return TrustProjection{RunID: runID, SystemAccountID: "sys", AccountID: "acct", RequestedModel: "gpt-5.6-sol", Report: report}
	}
	seedObservation("obs-1", "run-trust", "2026-09-05T10:00:00Z")
	seedObservation("obs-2", "run-trust", "2026-09-05T10:01:00Z")
	report := wbValidTrustReport()
	if err := store.ProjectTrust(ctx, projection("run-trust", report)); err != nil {
		t.Fatalf("首次投影失败: %v", err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_trust_observation_receipts`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("收据数量=%d err=%v", count, err)
	}
	var cursorID string
	if err := store.db.QueryRow(`SELECT cursor_id FROM model_trust_aggregation_state WHERE scope_key='model-trust-observation-aggregation'`).Scan(&cursorID); err != nil || cursorID != "obs-2" {
		t.Fatalf("聚合游标=%q err=%v", cursorID, err)
	}
	var consumed int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_check_observations WHERE aggregation_completed_at IS NOT NULL`).Scan(&consumed); err != nil || consumed != 2 {
		t.Fatalf("已消费观测=%d err=%v", consumed, err)
	}
	t.Run("exact replay is idempotent", func(t *testing.T) {
		if err := store.ProjectTrust(ctx, projection("run-trust", report)); err != nil {
			t.Fatalf("精确重放必须幂等: %v", err)
		}
	})
	t.Run("drifted replay conflicts", func(t *testing.T) {
		drifted := wbValidTrustReport()
		drifted.IdentityStatus = "suspected_downgrade"
		if err := store.ProjectTrust(ctx, projection("run-trust", drifted)); err == nil || !strings.Contains(err.Error(), "replay conflicts") {
			t.Fatalf("同游标漂移必须冲突: err=%v", err)
		}
	})
	t.Run("newer observation advances latest", func(t *testing.T) {
		seedObservation("obs-3", "run-trust", "2026-09-05T10:02:00Z")
		if err := store.ProjectTrust(ctx, projection("run-trust", report)); err != nil {
			t.Fatalf("更新投影失败: %v", err)
		}
		var observed int
		if err := store.db.QueryRow(`SELECT observation_count FROM model_account_trust_results WHERE system_account_id='sys' AND account_id='acct'`).Scan(&observed); err != nil || observed != 3 {
			t.Fatalf("最新观测数=%d err=%v", observed, err)
		}
	})
	t.Run("older projection is noop", func(t *testing.T) {
		if _, err := store.db.Exec(`DELETE FROM model_trust_observation_receipts WHERE observation_id='obs-0'`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`INSERT INTO model_check_observations (id,run_id,system_account_id,account_id,provider_code,requested_model,mapped_upstream_model,probe_family,observation_status,identity_status,mapping_status,protocol_status,evidence_coverage,created_at) VALUES ('obs-0','run-old','sys','acct','openai','gpt-5.6-sol','gpt-5.6-sol','stability','complete','consistent','direct','consistent',80,'2026-09-05T09:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		if err := store.ProjectTrust(ctx, projection("run-old", report)); err != nil {
			t.Fatalf("旧游标投影必须无副作用成功: %v", err)
		}
		var lastID string
		if err := store.db.QueryRow(`SELECT last_observed_id FROM model_account_trust_results WHERE system_account_id='sys' AND account_id='acct'`).Scan(&lastID); err != nil || lastID != "obs-3" {
			t.Fatalf("旧投影不得回退最新: last=%q err=%v", lastID, err)
		}
	})
	t.Run("invalid input and empty observations", func(t *testing.T) {
		if err := store.ProjectTrust(ctx, TrustProjection{}); err == nil {
			t.Fatal("空投影必须报错")
		}
		bad := projection("run-trust", report)
		bad.Report.IdentityStatus = "mystery"
		if err := store.ProjectTrust(ctx, bad); err == nil {
			t.Fatal("非法信任报告必须报错")
		}
		if err := store.ProjectTrust(ctx, projection("run-none", report)); err == nil || !strings.Contains(err.Error(), "no durable observations") {
			t.Fatalf("无观测必须报错: err=%v", err)
		}
	})
}

// appendObservationIdempotent 契约：存储/校验失败快速返回，
// 插入竞态时以行相等性决定是否接受重放。
func TestWBAppendObservationIdempotentErrorBranches(t *testing.T) {
	t.Run("nil store", func(t *testing.T) {
		if err := appendObservationIdempotent(context.Background(), nil, ObservationRecord{}); err == nil {
			t.Fatal("nil 存储必须报错")
		}
	})
	store := &Store{db: wbOpenMemoryDB(t, runtimeTestDDL()), mode: "sqlite"}
	defer store.Close()
	now := time.Date(2026, 9, 6, 9, 0, 0, 0, time.UTC)
	run := RunRecord{ID: "run-obs", SystemAccountID: "sys", ActorSystemAccountID: "actor", ProviderCode: "openai", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "full", TriggerKind: "manual", ProbeSetVersion: "probe-v1", StartedAt: now, RequestSummary: []byte(`{}`), PolicySnapshot: []byte(`{}`)}
	if err := store.CreateRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	record := ObservationRecord{ID: "run-obs-observation-family-0001", RunID: run.ID, SystemAccountID: "sys", AccountID: "acct", ProviderCode: "openai", RequestedModel: "gpt-5.6-sol", MappedUpstreamModel: "gpt-5.6-sol", ProbeFamily: "stability", ObservationStatus: "complete", IdentityStatus: "consistent", MappingStatus: "direct", ProtocolStatus: "consistent", EvidenceCoverage: 50, CreatedAt: now}
	if err := appendObservationIdempotent(context.Background(), store, record); err != nil {
		t.Fatalf("首次追加失败: %v", err)
	}
	t.Run("malformed record", func(t *testing.T) {
		bad := record
		bad.ID = "run-obs-observation-family-0002"
		bad.EvidenceCoverage = 101
		if err := appendObservationIdempotent(context.Background(), store, bad); err == nil {
			t.Fatal("非法覆盖度必须报错")
		}
	})
	t.Run("conflicting existing row", func(t *testing.T) {
		drift := record
		drift.ObservationStatus = "partial"
		if err := appendObservationIdempotent(context.Background(), store, drift); err == nil {
			t.Fatal("同 ID 不同事实必须报冲突")
		}
	})
}

// 数值/字符串证据提取契约：仅接受显式数值类型，缺失一律返回 false。
func TestWBEvidenceValueHelpers(t *testing.T) {
	if got, ok := numberValue(7); !ok || got != 7 {
		t.Fatalf("int 数值=%v ok=%v", got, ok)
	}
	var big int64 = 9
	if got, ok := numberValue(big); !ok || got != 9 {
		t.Fatalf("int64 数值=%v ok=%v", got, ok)
	}
	if got, ok := numberValue(2.5); !ok || got != 2.5 {
		t.Fatalf("float 数值=%v ok=%v", got, ok)
	}
	if _, ok := numberValue("7"); ok {
		t.Fatal("字符串不得当作数值")
	}
	if maxZero(-3) != 0 || maxZero(3) != 3 {
		t.Fatal("maxZero 必须钳制负数")
	}
	if got := evidenceStringValue(nil, "k"); got != "" {
		t.Fatalf("nil 证据字符串=%q", got)
	}
}

// validateBuiltRequest 契约：scope、目标快照与 profile 必须与命令一致。
func TestWBValidateBuiltRequestTable(t *testing.T) {
	scope := ManagementScope{ActorSystemAccountID: "actor", SelectedSystemAccountID: "sys"}
	command := RunCommand{TargetType: "account", TargetID: "acct", Model: "m", Profile: "quick"}
	valid := RunRequest{SystemAccountID: "sys", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct", Model: "m", Profile: "quick"}
	if err := validateBuiltRequest(scope, command, valid); err != nil {
		t.Fatalf("合法请求不得报错: %v", err)
	}
	cases := []struct {
		name  string
		scope ManagementScope
		mut   func(*RunRequest)
		need  string
	}{
		{name: "invalid scope", scope: ManagementScope{}, mut: func(r *RunRequest) {}, need: "scope 不完整"},
		{name: "scope mismatch", scope: scope, mut: func(r *RunRequest) { r.SystemAccountID = "other" }, need: "不一致"},
		{name: "actor mismatch", scope: scope, mut: func(r *RunRequest) { r.ActorSystemAccountID = "other" }, need: "actor"},
		{name: "target type drift", scope: scope, mut: func(r *RunRequest) { r.TargetType = "group" }, need: "不一致"},
		{name: "profile invalid", scope: scope, mut: func(r *RunRequest) { r.Profile = "fast" }, need: "profile"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := valid
			tc.mut(&request)
			if err := validateBuiltRequest(tc.scope, command, request); err == nil || !strings.Contains(err.Error(), tc.need) {
				t.Fatalf("err=%v want 包含 %q", err, tc.need)
			}
		})
	}
}

// 授权适配器契约：未初始化的认证器必须失败关闭。
func TestWBAuthorizeAdaptersRequireAuthenticator(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/run", nil)
	if _, err := NewAdminAuthorize(nil)(context.Background(), request); err == nil {
		t.Fatal("nil 管理认证器必须报错")
	}
	if _, err := NewSelfAuthorize(nil)(context.Background(), request); err == nil {
		t.Fatal("nil self 认证器必须报错")
	}
	if _, err := NewAdminAuthorize(nil)(context.Background(), nil); err == nil {
		t.Fatal("nil 请求必须报错")
	}
}

// SSE/请求错误辅助契约：写失败与空错误都必须保持稳定语义。
func TestWBHTTPWriteHelpersContracts(t *testing.T) {
	recorder := httptest.NewRecorder()
	if err := writeSSE(recorder, recorder, "progress", make(chan int)); err == nil {
		t.Fatal("不可序列化载荷必须报错")
	}
	if (&RequestError{}).Error() != "" {
		t.Fatal("空 RequestError 消息必须为空串")
	}
	handler := &HTTPHandler{}
	if got := handler.maxBody(); got != 512<<10 {
		t.Fatalf("默认请求体上限=%d", got)
	}
	if got := handler.heartbeat(); got != 10*time.Second {
		t.Fatalf("默认心跳=%s", got)
	}
	// 分页/选项查询中的未知字段拒绝由 decodeOwnerJSON 兜底。
	if err := decodeOwnerJSON(httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`[]`)), 1024, &struct{}{}); err == nil {
		t.Fatal("非对象请求体必须报错")
	}
	if err := decodeOwnerJSON(httptest.NewRequest(http.MethodPost, "/x", nil), 1024, &struct{}{}); err == nil {
		t.Fatal("空请求体必须报错")
	}
}

// normalizeJSON 契约：非法或非对象 JSON 一律折叠为空对象。
func TestWBNormalizeJSONCollapsesInvalidPayloads(t *testing.T) {
	for name, payload := range map[string]string{
		"invalid": `{`,
		"array":   `[1,2]`,
		"string":  `"x"`,
	} {
		if got := normalizeJSON([]byte(payload)); string(got) != "{}" {
			t.Fatalf("%s 折叠结果=%s", name, got)
		}
	}
	// 敏感键必须被打码而不是丢弃。
	redacted := normalizeJSON([]byte(`{"api_key":"sk-secret","nested":{"password":"x"},"items":[1,2]}`))
	var out map[string]any
	if err := json.Unmarshal(redacted, &out); err != nil {
		t.Fatal(err)
	}
	if out["api_key"] != "[redacted]" {
		t.Fatalf("api_key=%v", out["api_key"])
	}
}

// 运行/条目/观测校验与追加的失败关闭分支。
func TestWBRunRecordValidationAndAppendBranches(t *testing.T) {
	store := &Store{db: wbOpenMemoryDB(t, runtimeTestDDL()), mode: "sqlite"}
	defer store.Close()
	ctx := context.Background()
	base := RunRecord{ID: "run-v", SystemAccountID: "sys", ActorSystemAccountID: "actor", ProviderCode: "openai", TargetType: "account", TargetID: "acct", Model: "m", Profile: "quick", TriggerKind: "manual", ProbeSetVersion: "probe-v1", StartedAt: time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC), RequestSummary: []byte(`{}`), PolicySnapshot: []byte(`{}`)}
	if err := store.CreateRun(ctx, base); err != nil {
		t.Fatalf("合法 run 创建失败: %v", err)
	}
	t.Run("create run validation", func(t *testing.T) {
		missing := base
		missing.ID = "run-v2"
		missing.ProviderCode = " "
		if err := store.CreateRun(ctx, missing); err == nil {
			t.Fatal("缺失 provider 必须报错")
		}
		zero := base
		zero.ID = "run-v3"
		zero.StartedAt = time.Time{}
		if err := store.CreateRun(ctx, zero); err == nil {
			t.Fatal("零值时间必须报错")
		}
		badJSON := base
		badJSON.ID = "run-v4"
		badJSON.RequestSummary = []byte(`{`)
		if err := store.CreateRun(ctx, badJSON); err == nil {
			t.Fatal("非法请求摘要必须报错")
		}
	})
	t.Run("append observation branches", func(t *testing.T) {
		observation := ObservationRecord{ID: "obs-v1", RunID: base.ID, SystemAccountID: "sys", AccountID: "acct", ProviderCode: "openai", RequestedModel: "m", MappedUpstreamModel: "m", ProbeFamily: "stability", ObservationStatus: "complete", IdentityStatus: "consistent", MappingStatus: "direct", ProtocolStatus: "consistent", EvidenceCoverage: 10, CreatedAt: base.StartedAt}
		if err := store.AppendObservation(ctx, observation); err != nil {
			t.Fatalf("合法观测追加失败: %v", err)
		}
		if err := store.AppendObservation(ctx, ObservationRecord{RunID: base.ID}); err == nil {
			t.Fatal("缺失身份的观测必须报错")
		}
		missing := observation
		missing.ID = "obs-v2"
		missing.RunID = "run-none"
		if err := store.AppendObservation(ctx, missing); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("缺失 run 必须报错: err=%v", err)
		}
	})
	t.Run("validate item evidence", func(t *testing.T) {
		if err := validateItem(ItemRecord{ID: "i", RunID: "r", ItemKey: "k", ItemType: "t", Status: ItemSkipped, EvidenceSummary: `[1]`}); err == nil {
			t.Fatal("非对象证据必须报错")
		}
		if err := validateItem(ItemRecord{ID: "i", RunID: "r", ItemKey: "k", ItemType: "t", Status: ItemStatus("mystery")}); err == nil {
			t.Fatal("非法状态必须报错")
		}
	})
}

// 代理客户端与 Codex 适配器分支契约。
func TestWBProxyAndAdapterBranches(t *testing.T) {
	validProxy := buildProxyClientArgs{profile: sql.NullString{String: "proxy-1", Valid: true}, enabled: sql.NullBool{Bool: true, Valid: true}, host: sql.NullString{String: "proxy.example", Valid: true}, port: sql.NullInt64{Int64: 8080, Valid: true}}
	if client, err := buildProxyClient("secret", validProxy.profile, validProxy.enabled, sql.NullString{String: "http", Valid: true}, validProxy.host, validProxy.port, sql.NullString{}, sql.NullString{}); err != nil || client == nil {
		t.Fatalf("合法代理必须返回 client: %v", err)
	}
	cases := []struct {
		name  string
		mut   func(*buildProxyClientArgs)
		proxy sql.NullString
	}{
		{name: "disabled", mut: func(a *buildProxyClientArgs) { a.enabled = sql.NullBool{} }},
		{name: "missing host", mut: func(a *buildProxyClientArgs) { a.host = sql.NullString{} }},
		{name: "port out of range", mut: func(a *buildProxyClientArgs) { a.port = sql.NullInt64{Int64: 0, Valid: true} }},
		{name: "unsupported scheme", proxy: sql.NullString{String: "gopher", Valid: true}},
		{name: "username without password", proxy: sql.NullString{String: "http", Valid: true}, mut: func(a *buildProxyClientArgs) { a.username = sql.NullString{String: "user", Valid: true} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := validProxy
			if tc.mut != nil {
				tc.mut(&args)
			}
			proxyType := tc.proxy
			if !proxyType.Valid {
				proxyType = sql.NullString{String: "http", Valid: true}
			}
			if _, err := buildProxyClient("secret", args.profile, args.enabled, proxyType, args.host, args.port, args.username, args.password); err == nil {
				t.Fatalf("%s 必须报错", tc.name)
			}
		})
	}
	t.Run("no proxy configured", func(t *testing.T) {
		if client, err := buildProxyClient("secret", sql.NullString{}, sql.NullBool{}, sql.NullString{}, sql.NullString{}, sql.NullInt64{}, sql.NullString{}, sql.NullString{}); err != nil || client != nil {
			t.Fatalf("未配置代理必须返回 nil client: %v %v", client, err)
		}
	})
	t.Run("codex adapter contracts", func(t *testing.T) {
		if adapter, err := openAIOAuthCodexAdapter("openai", "profile_other", "oauth", modelcheckprofile.ProtocolAnthropic, "messages_json"); err != nil || adapter != "" {
			t.Fatalf("非 OpenAI profile 的 OAuth 必须走普通通道: adapter=%q err=%v", adapter, err)
		}
		if _, err := openAIOAuthCodexAdapter("openai", "profile_other", "oauth", modelcheckprofile.ProtocolOpenAIChat, "chat_json"); err == nil {
			t.Fatal("OpenAI 兼容 profile 的 OAuth 必须报错")
		}
		if _, err := openAIOAuthCodexAdapter("gpt", "profile_gpt_openai_v1", "oauth", modelcheckprofile.ProtocolOpenAIResponses, "chat_json"); err == nil {
			t.Fatal("Codex 不支持的端点模式必须报错")
		}
	})
}

type buildProxyClientArgs struct {
	profile  sql.NullString
	enabled  sql.NullBool
	host     sql.NullString
	port     sql.NullInt64
	username sql.NullString
	password sql.NullString
}

// targetSystemAccountID 与 readPolicy 分支契约。
func TestWBBusinessSourceTargetOwnerAndPolicy(t *testing.T) {
	db := wbOpenMemoryDB(t, []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY, system_account_id TEXT, deleted_at TEXT)`,
		`CREATE TABLE model_quality_policies (system_account_id TEXT PRIMARY KEY, revision INTEGER, profile TEXT, manual_enforcement_enabled INTEGER, penalty_threshold INTEGER, penalty_action TEXT, recovery_interval_minutes INTEGER, custom_question_ids TEXT)`,
	})
	source, err := NewBusinessTargetSource(db, false, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.targetSystemAccountID(context.Background(), " "); err == nil {
		t.Fatal("空目标必须报错")
	}
	if _, err := source.targetSystemAccountID(context.Background(), "ghost"); err == nil || !strings.Contains(err.Error(), "outside scope") {
		t.Fatalf("缺失目标必须返回 404 语义错误: err=%v", err)
	}
	if _, err := db.Exec(`INSERT INTO accounts VALUES ('acct', '  ', NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.targetSystemAccountID(context.Background(), "acct"); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("空白租户必须报错: err=%v", err)
	}
	if _, err := db.Exec(`INSERT INTO model_quality_policies VALUES ('sys',2,'full',1,80,'quality_isolate',20,NULL)`); err != nil {
		t.Fatal(err)
	}
	profile, revision, manual, threshold, action, interval, ids, err := source.readPolicy(context.Background(), "sys")
	if err != nil || profile != "full" || revision != "2" || !manual || threshold != 80 || action != "quality_isolate" || interval != 20 || len(ids) != 0 {
		t.Fatalf("策略读取=%s/%s/%v/%d/%s/%d/%v err=%v", profile, revision, manual, threshold, action, interval, ids, err)
	}
	if _, err := db.Exec(`UPDATE model_quality_policies SET penalty_threshold=10 WHERE system_account_id='sys'`); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, _, _, _, err := source.readPolicy(context.Background(), "sys"); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("越界策略必须报错: err=%v", err)
	}
	if _, _, _, _, _, _, _, err := source.readPolicy(context.Background(), "ghost"); err != nil {
		t.Fatalf("缺失策略必须回退默认: err=%v", err)
	}
}

// 布尔/空值转换与 nil 管理器等微型分支。
func TestWBMiscBranches(t *testing.T) {
	if boolIntQuality(false) != 0 || boolIntGateway(false) != 0 {
		t.Fatal("false 必须映射 0")
	}
	if defaultProfile(" ") != "quick" {
		t.Fatal("空白 profile 必须回退 quick")
	}
	if _, err := NewBusinessQualityManager(nil, false); err == nil {
		t.Fatal("nil 数据库必须报错")
	}
	store := &Store{}
	if err := store.Close(); err != nil {
		t.Fatal("未打开存储的关闭必须幂等成功")
	}
	if got := (&Store{mode: "postgres", schema: "juhe_j3b"}).table("model_check_inputs"); got != "juhe_j3b.model_check_inputs" {
		t.Fatalf("postgres 表名=%q", got)
	}
}

// gatewayMinute / validateGatewayWindow 分支契约。
func TestWBGatewayWindowValidation(t *testing.T) {
	cases := []struct {
		value string
		ok    bool
	}{
		{"00:00", true}, {"23:59", true}, {"24:00", false}, {"ab:cd", false}, {"1:00", false}, {"12:60", false}, {"", false},
	}
	for _, tc := range cases {
		if _, ok := gatewayMinute(tc.value); ok != tc.ok {
			t.Fatalf("gatewayMinute(%q) ok=%v want=%v", tc.value, ok, tc.ok)
		}
	}
	if err := validateGatewayWindow(gatewayWindow{Start: "bad", End: "10:00"}, true); err == nil {
		t.Fatal("非法开始必须拒绝")
	}
	if err := validateGatewayWindow(gatewayWindow{Start: "10:00", End: "10:00"}, true); err == nil {
		t.Fatal("开始等于结束必须拒绝")
	}
	if err := validateGatewayWindow(gatewayWindow{Start: "10:00", End: "11:00"}, true); err == nil {
		t.Fatal("缺天数必须拒绝")
	}
	if err := validateGatewayWindow(gatewayWindow{Start: "10:00", End: "11:00", Days: []int{0}}, true); err == nil {
		t.Fatal("非法天数必须拒绝")
	}
	if err := validateGatewayWindow(gatewayWindow{Start: "10:00", End: "11:00"}, false); err != nil {
		t.Fatalf("异常窗口不要求天数: %v", err)
	}
}

// memorySchedulerSource 契约：按任务族认领并移除，limit 生效。
func TestWBMemorySchedulerSource(t *testing.T) {
	source := &memorySchedulerSource{tasks: []ScheduleTask{
		{ID: "a", Kind: SchedulerScheduled},
		{ID: "b", Kind: SchedulerScheduled},
		{ID: "c", Kind: SchedulerQualityRecovery},
	}}
	tasks, err := source.Claim(context.Background(), SchedulerScheduled, time.Now(), 1)
	if err != nil || len(tasks) != 1 || tasks[0].ID != "a" {
		t.Fatalf("认领结果=%+v err=%v", tasks, err)
	}
	tasks, err = source.Claim(context.Background(), SchedulerQualityRecovery, time.Now(), 5)
	if err != nil || len(tasks) != 1 || tasks[0].ID != "c" {
		t.Fatalf("恢复认领=%+v err=%v", tasks, err)
	}
}

// SQLSchedulerSource.Fail 分支契约。
func TestWBSQLSchedulerFailBranches(t *testing.T) {
	source := &SQLSchedulerSource{}
	if err := source.Fail(context.Background(), ScheduleTask{ID: "x"}, errors.New("boom")); err == nil {
		t.Fatal("未初始化的源必须报错")
	}
	j3b := wbOpenMemoryDB(t, []string{`CREATE TABLE model_check_scheduler_tasks (id TEXT PRIMARY KEY,kind TEXT,due_at TEXT,claim_owner TEXT,claim_until TEXT,fence_token INTEGER,state TEXT,last_error TEXT,completed_at TEXT,payload TEXT,updated_at TEXT)`})
	owned := &SQLSchedulerSource{Store: &Store{db: j3b, mode: "sqlite"}, OwnerID: "owner"}
	if err := owned.Fail(context.Background(), ScheduleTask{ID: "ghost", OwnerID: "owner", FenceToken: 1}, errors.New("boom")); err == nil {
		t.Fatal("缺失任务必须报错")
	}
	if err := owned.Complete(context.Background(), ScheduleTask{ID: "ghost", OwnerID: "owner", FenceToken: 1}); err == nil {
		t.Fatal("缺失任务的完成必须报错")
	}
}

// CompareLatestWins 契约：observed_at 优先、runID 决胜，scope 不一致报错。
func TestWBCompareLatestWinsTable(t *testing.T) {
	now := time.Date(2026, 9, 6, 11, 0, 0, 0, time.UTC)
	candidate := HealthFact{AccountID: "acct", StatHour: "2026-09-06T11", RunID: "run-b", ObservedAt: now}
	current := HealthFact{AccountID: "acct", StatHour: "2026-09-06T11", RunID: "run-a", ObservedAt: now.Add(-time.Minute)}
	if got, err := CompareLatestWins(candidate, current); err != nil || got != 1 {
		t.Fatalf("更新候选=%d err=%v", got, err)
	}
	if got, err := CompareLatestWins(current, candidate); err != nil || got != -1 {
		t.Fatalf("陈旧候选=%d err=%v", got, err)
	}
	same := HealthFact{AccountID: "acct", StatHour: "2026-09-06T11", RunID: "run-a", ObservedAt: now}
	if got, err := CompareLatestWins(HealthFact{AccountID: "acct", StatHour: "2026-09-06T11", RunID: "run-b", ObservedAt: now}, same); err != nil || got != 1 {
		t.Fatalf("同刻后位 runID=%d err=%v", got, err)
	}
	if got, err := CompareLatestWins(same, candidate); err != nil || got != -1 {
		t.Fatalf("同刻前位 runID=%d err=%v", got, err)
	}
	if _, err := CompareLatestWins(HealthFact{}, HealthFact{}); err == nil {
		t.Fatal("不完整候选必须报错")
	}
	foreign := HealthFact{AccountID: "other", StatHour: "2026-09-06T12", RunID: "run-a", ObservedAt: now}
	if _, err := CompareLatestWins(candidate, foreign); err == nil {
		t.Fatal("scope 不一致必须报错")
	}
	emptyCurrent := HealthFact{}
	if got, err := CompareLatestWins(candidate, emptyCurrent); err != nil || got != 1 {
		t.Fatalf("空当前=%d err=%v", got, err)
	}
}
