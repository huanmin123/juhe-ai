package modelcheckowner

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
	_ "modernc.org/sqlite"
)

// wbOpenMemoryDB 打开一个本测试专属的 modernc.org/sqlite 内存库。
// 共享缓存 + 单连接与生产 SQLite owner 的单连接语义保持一致，
// 避免多连接下的 database is locked 竞争。
func wbOpenMemoryDB(t *testing.T, ddl []string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:modelcheckowner-"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range ddl {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("执行 DDL %q 失败: %v", statement, err)
		}
	}
	return db
}

// Store.bind 的业务契约：SQLite 保持 ? 占位符，PostgreSQL 必须改写为 $N，
// 否则 pgx 驱动会因为参数不匹配拒绝执行。
func TestWBStoreBindRewritesPlaceholdersOnlyForPostgres(t *testing.T) {
	t.Run("postgres", func(t *testing.T) {
		store := &Store{mode: "postgres", schema: "juhe_j3b"}
		got := store.bind("SELECT a FROM t WHERE x=? AND y=?")
		if got != "SELECT a FROM t WHERE x=$1 AND y=$2" {
			t.Fatalf("PostgreSQL 占位符改写结果=%q", got)
		}
		if store.forUpdate() != " FOR UPDATE" {
			t.Fatalf("PostgreSQL 读取必须追加 FOR UPDATE: %q", store.forUpdate())
		}
	})
	t.Run("sqlite", func(t *testing.T) {
		store := &Store{mode: "sqlite"}
		query := "SELECT a FROM t WHERE x=?"
		if store.bind(query) != query {
			t.Fatalf("SQLite 占位符必须原样保留: %q", store.bind(query))
		}
		if store.forUpdate() != "" {
			t.Fatalf("SQLite 禁止追加 FOR UPDATE: %q", store.forUpdate())
		}
	})
}

// 表名限定契约：PostgreSQL 必须落在 juhe_j3b schema，SQLite 使用本地表名。
func TestWBStoreQualifiedTableNamesFollowMode(t *testing.T) {
	postgres := &Store{mode: "postgres", schema: "juhe_j3b"}
	if got := postgres.schedulerTaskTable(); got != "juhe_j3b.model_check_scheduler_tasks" {
		t.Fatalf("PostgreSQL 调度表名=%q", got)
	}
	if got := postgres.healthTable(); got != "juhe_j3b.account_quality_health_hourly" {
		t.Fatalf("PostgreSQL 健康表名=%q", got)
	}
	if got := postgres.tokenInterceptBaselineTable(); got != "juhe_j3b.model_token_intercept_baseline_versions" {
		t.Fatalf("PostgreSQL 基线表名=%q", got)
	}
	sqlite := &Store{mode: "sqlite"}
	if got := sqlite.schedulerTaskTable(); got != "model_check_scheduler_tasks" {
		t.Fatalf("SQLite 调度表名=%q", got)
	}
	if got := sqlite.healthTable(); got != "account_quality_health_hourly" {
		t.Fatalf("SQLite 健康表名=%q", got)
	}
}

// parseDBTime 必须兼容驱动可能返回的三种时间表示，并对不支持的类型报错。
func TestWBParseDBTimeAcceptsDriverRepresentations(t *testing.T) {
	want := time.Date(2026, 9, 1, 10, 30, 0, 0, time.UTC)
	t.Run("time value", func(t *testing.T) {
		got, err := parseDBTime(want)
		if err != nil || !got.UTC().Equal(want) {
			t.Fatalf("time.Time 直通失败: got=%v err=%v", got, err)
		}
	})
	t.Run("string value", func(t *testing.T) {
		got, err := parseDBTime(want.Format(time.RFC3339Nano))
		if err != nil || !got.UTC().Equal(want) {
			t.Fatalf("字符串时间解析失败: got=%v err=%v", got, err)
		}
	})
	t.Run("bytes value", func(t *testing.T) {
		got, err := parseDBTime([]byte(want.Format(time.RFC3339Nano)))
		if err != nil || !got.UTC().Equal(want) {
			t.Fatalf("字节时间解析失败: got=%v err=%v", got, err)
		}
	})
	t.Run("invalid string", func(t *testing.T) {
		if _, err := parseDBTime("not-a-time"); err == nil {
			t.Fatal("非法时间字符串必须报错")
		}
	})
	t.Run("unsupported type", func(t *testing.T) {
		if _, err := parseDBTime(42); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("不支持的时间类型必须报错: err=%v", err)
		}
	})
}

// 方言片段契约：Business 读写适配器在 PostgreSQL 下生成限定表名、$N 占位符
// 和 timestamptz 比较；SQLite 下保持本地名与 datetime 比较。
func TestWBBusinessDialectFragmentsFollowDatabase(t *testing.T) {
	t.Run("target source literals", func(t *testing.T) {
		postgres := &BusinessTargetSource{postgres: true}
		if got := postgres.boolLiteral(true); got != "TRUE" || postgres.boolLiteral(false) != "FALSE" {
			t.Fatalf("PostgreSQL 布尔字面量 true=%q false=%q", postgres.boolLiteral(true), postgres.boolLiteral(false))
		}
		if got := postgres.placeholder(2); got != "$2" {
			t.Fatalf("PostgreSQL 占位符=%q", got)
		}
		if got := postgres.expiryAfterNow("a.account_expires_at"); !strings.Contains(got, "::timestamptz") {
			t.Fatalf("PostgreSQL 过期比较=%q", got)
		}
		sqlite := &BusinessTargetSource{}
		if got := sqlite.boolLiteral(false); got != "0" {
			t.Fatalf("SQLite 布尔 false=%q", got)
		}
		if got := sqlite.boolLiteral(true); got != "1" {
			t.Fatalf("SQLite 布尔 true=%q", got)
		}
		if got := sqlite.cooldownClear("a.cooldown_until"); !strings.Contains(got, "datetime(") {
			t.Fatalf("SQLite 冷却比较=%q", got)
		}
	})
	t.Run("recovery applier", func(t *testing.T) {
		postgres := &BusinessRecoveryApplier{postgres: true}
		if got := postgres.bind("UPDATE t SET a=? WHERE b=?"); got != "UPDATE t SET a=$1 WHERE b=$2" {
			t.Fatalf("恢复适配器 PostgreSQL bind=%q", got)
		}
		if got := postgres.table("accounts"); got != "juhe_business.accounts" {
			t.Fatalf("恢复适配器 PostgreSQL 表名=%q", got)
		}
		if got := (&BusinessRecoveryApplier{}).table("accounts"); got != "accounts" {
			t.Fatalf("恢复适配器 SQLite 表名=%q", got)
		}
	})
	t.Run("quality manager", func(t *testing.T) {
		postgres := &BusinessQualityManager{postgres: true}
		if got := postgres.bind("SELECT ?"); got != "SELECT $1" {
			t.Fatalf("质量管理 PostgreSQL bind=%q", got)
		}
		if got := postgres.table("model_quality_schedules"); got != "juhe_business.model_quality_schedules" {
			t.Fatalf("质量管理 PostgreSQL 表名=%q", got)
		}
	})
	t.Run("scheduler source", func(t *testing.T) {
		postgres := &BusinessSchedulerSource{Postgres: true}
		if got := postgres.bind("WHERE a=? AND b=?"); got != "WHERE a=$1 AND b=$2" {
			t.Fatalf("调度源 PostgreSQL bind=%q", got)
		}
		if got := postgres.table("model_quality_schedules"); got != "juhe_business.model_quality_schedules" {
			t.Fatalf("调度源 PostgreSQL 表名=%q", got)
		}
		if got := (&BusinessSchedulerSource{}).table("accounts"); got != "accounts" {
			t.Fatalf("调度源 SQLite 表名=%q", got)
		}
	})
	t.Run("enforcement applier", func(t *testing.T) {
		postgres := &BusinessEnforcementApplier{postgres: true}
		if got := postgres.placeholder(1); got != "$1" {
			t.Fatalf("执行适配器 PostgreSQL 占位符=%q", got)
		}
		if got := postgres.placeholders(3); got != "$1,$2,$3" {
			t.Fatalf("执行适配器 PostgreSQL 占位符串=%q", got)
		}
		if got := postgres.table("accounts"); got != "juhe_business.accounts" {
			t.Fatalf("执行适配器 PostgreSQL 表名=%q", got)
		}
		sqlite := &BusinessEnforcementApplier{}
		if got := sqlite.placeholders(2); got != "?,?" {
			t.Fatalf("执行适配器 SQLite 占位符串=%q", got)
		}
	})
}

// 版本化模型容量快照必须暴露稳定版本号，供 full 探针冻结。
func TestWBVersionedModelLimitsReportsStableVersion(t *testing.T) {
	limits, err := NewVersionedModelLimits(wbOpenMemoryDB(t, nil), false)
	if err != nil {
		t.Fatalf("构造 VersionedModelLimits 失败: %v", err)
	}
	if got := limits.Version(); got != "business-provider-model-catalog-v1" {
		t.Fatalf("模型容量快照版本=%q", got)
	}
}

// hasTrustText 契约：只有存在且非空字符串的信任报告字段才算有效文本。
func TestWBHasTrustTextRequiresNonEmptyJSONString(t *testing.T) {
	cases := []struct {
		name  string
		trust map[string]json.RawMessage
		want  bool
	}{
		{name: "nil map", trust: nil, want: false},
		{name: "missing field", trust: map[string]json.RawMessage{}, want: false},
		{name: "non string value", trust: map[string]json.RawMessage{"observedModel": json.RawMessage(`123`)}, want: false},
		{name: "blank string", trust: map[string]json.RawMessage{"observedModel": json.RawMessage(`"   "`)}, want: false},
		{name: "valid string", trust: map[string]json.RawMessage{"observedModel": json.RawMessage(`"gpt-5.6"`)}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasTrustText(tc.trust, "observedModel"); got != tc.want {
				t.Fatalf("hasTrustText=%v want=%v", got, tc.want)
			}
		})
	}
}

// decryptCredential 契约：明文凭据直通；结构化 JSON 依次接受
// api_key、access_token、token 字段；其余一律拒绝。
func TestWBDecryptCredentialExtractsSupportedTokenFields(t *testing.T) {
	cases := []struct {
		name      string
		plaintext string
		want      string
		wantErr   string
	}{
		{name: "plain secret", plaintext: "sk-plain", want: "sk-plain"},
		{name: "api_key field", plaintext: `{"api_key":"key-1"}`, want: "key-1"},
		{name: "access_token field", plaintext: `{"access_token":"token-1"}`, want: "token-1"},
		{name: "token field", plaintext: `{"token":"token-2"}`, want: "token-2"},
		{name: "trimmed value", plaintext: `{"api_key":"  key-2  "}`, want: "key-2"},
		{name: "no supported field", plaintext: `{"metadata":"x"}`, wantErr: "no supported token field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envelope := testCredentialEnvelope(t, "secret", tc.plaintext)
			got, err := decryptCredential("secret", envelope)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("必须拒绝并提示 %q: got=%q err=%v", tc.wantErr, got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("decryptCredential=%q err=%v want=%q", got, err, tc.want)
			}
		})
	}
	t.Run("invalid envelope", func(t *testing.T) {
		if _, err := decryptCredential("secret", "not-an-envelope"); err == nil {
			t.Fatal("非法信封必须报错")
		}
	})
}

// decryptCredentialStringField 契约：按字段名读取字符串，未命中返回 false
// 而不是错误，便于调用方区分"缺字段"与"凭据损坏"。
func TestWBDecryptCredentialStringFieldReadsNamedField(t *testing.T) {
	envelope := testCredentialEnvelope(t, "secret", `{"oauth_type":"claude","api_key":"key"}`)
	got, found, err := decryptCredentialStringField("secret", envelope, "oauth_type")
	if err != nil || !found || got != "claude" {
		t.Fatalf("读取 oauth_type=(%q,%v,%v)", got, found, err)
	}
	if _, found, err := decryptCredentialStringField("secret", envelope, "quota_project_id"); err != nil || found {
		t.Fatalf("缺失字段必须返回 found=false: err=%v", err)
	}
	if _, found, err := decryptCredentialStringField("secret", testCredentialEnvelope(t, "secret", "plain"), "api_key"); err != nil || found {
		t.Fatalf("非结构化明文必须返回 found=false: err=%v", err)
	}
	if _, _, err := decryptCredentialStringField("secret", "broken", "api_key"); err == nil {
		t.Fatal("非法信封必须报错")
	}
}

// parseCredentialFields 契约：JSON 对象以外的任何合法或非法 JSON 都不能
// 被当作 bearer token。
func TestWBParsedCredentialFieldsRejectsNonObjectJSON(t *testing.T) {
	cases := []struct {
		name       string
		plaintext  string
		wantErr    string
		wantStruct bool
	}{
		{name: "empty", plaintext: "   ", wantErr: "credential is empty"},
		{name: "malformed object", plaintext: `{"api_key":1`, wantErr: "credential JSON is invalid", wantStruct: true},
		{name: "malformed array", plaintext: `[1,2`, wantErr: "credential JSON is invalid", wantStruct: true},
		{name: "malformed quoted string", plaintext: `"abc`, wantErr: "credential JSON is invalid", wantStruct: true},
		{name: "valid array", plaintext: `[1,2]`, wantErr: "must be an object", wantStruct: true},
		{name: "valid quoted string", plaintext: `"sk-quoted"`, wantErr: "must be an object", wantStruct: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields, structured, err := parseCredentialFields(tc.plaintext)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("必须报错包含 %q: fields=%v structured=%v err=%v", tc.wantErr, fields, structured, err)
			}
			if structured != tc.wantStruct {
				t.Fatalf("structured=%v want=%v", structured, tc.wantStruct)
			}
		})
	}
	t.Run("plain secret", func(t *testing.T) {
		fields, structured, err := parseCredentialFields("sk-plain")
		if err != nil || structured || fields != nil {
			t.Fatalf("明文凭据必须按非结构化处理: fields=%v structured=%v err=%v", fields, structured, err)
		}
	})
}

// resolveConfiguredUpstreamModel 是严格映射解析器的兼容 shim，
// 必须与 resolveConfiguredUpstreamModelMapping 返回同一上游模型。
func TestWBResolveConfiguredUpstreamModelShimDelegatesToStrictMapping(t *testing.T) {
	profile, ok := modelcheckprofile.Find("openai", "profile_openai_openai_v1")
	if !ok {
		t.Fatal("测试前提失败：找不到 openai responses 画像")
	}
	db := wbOpenMemoryDB(t, []string{
		`CREATE TABLE account_supported_models (account_id TEXT, model TEXT)`,
		`CREATE TABLE account_model_mappings (account_id TEXT, source_model TEXT, source_endpoint_family TEXT, upstream_model TEXT, upstream_endpoint_family TEXT, enabled INTEGER)`,
	})
	model := "gpt-5.6-sol"
	if got, err := resolveConfiguredUpstreamModel(context.Background(), db, false, "acct-1", profile, model); err != nil || got != model {
		t.Fatalf("无映射时必须返回请求模型: got=%q err=%v", got, err)
	}
	if _, err := db.Exec(`INSERT INTO account_model_mappings VALUES ('acct-1','gpt-5.6-sol','responses','gpt-5.6-terra','responses',1)`); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveConfiguredUpstreamModel(context.Background(), db, false, "acct-1", profile, model); err != nil || got != "gpt-5.6-terra" {
		t.Fatalf("启用映射时必须返回配置的上游模型: got=%q err=%v", got, err)
	}
	if _, err := db.Exec(`DROP TABLE account_model_mappings`); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveConfiguredUpstreamModel(context.Background(), db, false, "acct-1", profile, model); err == nil || !strings.Contains(err.Error(), "model mapping") {
		t.Fatalf("映射查询失败必须向上传播错误: err=%v", err)
	}
}

// 可用时间窗 dateRange 契约：空白边界合法，非法日期或 start>end 必须拒绝。
func TestWBRecoveryAvailabilityDateRangeAllowsBlankBounds(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	schedule := func(dateRange string) string {
		return `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"00:00","end":"23:59"}],"dateRange":` + dateRange + `}`
	}
	allowed, err := availabilityAllowedGateway(schedule(`{"startDate":"","endDate":""}`), now)
	if err != nil || !allowed {
		t.Fatalf("空白日期边界必须允许探测: allowed=%v err=%v", allowed, err)
	}
	allowed, err = availabilityAllowedGateway(schedule(`{"startDate":"2026-01-01","endDate":""}`), now)
	if err != nil || !allowed {
		t.Fatalf("仅起始边界必须允许探测: allowed=%v err=%v", allowed, err)
	}
	for name, raw := range map[string]string{
		"invalid start": schedule(`{"startDate":"2026-13-01","endDate":""}`),
		"invalid end":   schedule(`{"startDate":"","endDate":"2026-02-31"}`),
		"inverted":      schedule(`{"startDate":"2026-12-01","endDate":"2026-01-01"}`),
	} {
		if _, err := availabilityAllowedGateway(raw, now); err == nil || !strings.Contains(err.Error(), "date range") {
			t.Fatalf("%s 必须拒绝: err=%v", name, err)
		}
	}
}

// CheckBusinessPostgresSchema 在触达数据库之前必须先校验 nil 句柄和 schema 名；
// 真实 PostgreSQL 校验需要 PG 实例，不属于本单测目标。
func TestWBCheckBusinessPostgresSchemaFailsClosedBeforeDatabaseAccess(t *testing.T) {
	if err := CheckBusinessPostgresSchema(context.Background(), nil, "juhe_business"); err == nil || !strings.Contains(err.Error(), "database is nil") {
		t.Fatalf("nil 数据库必须报错: err=%v", err)
	}
	db := wbOpenMemoryDB(t, nil)
	if err := CheckBusinessPostgresSchema(context.Background(), db, "juhe-business"); err == nil || !strings.Contains(err.Error(), "schema name is invalid") {
		t.Fatalf("非法 schema 名必须报错: err=%v", err)
	}
	// information_schema 在 SQLite 中不存在，必须以可读错误失败关闭而不是 panic。
	if err := CheckBusinessPostgresSchema(context.Background(), db, "juhe_business"); err == nil {
		t.Fatal("非 PostgreSQL 句柄必须失败关闭")
	}
}
