package connectivityhealth

// 授权实例连通性候选的 PG 集成回归（2026-11-21）：
//   - 媒体域授权实例没有自身连接行时仍按来源账户连接入选并完成；
//   - chat 域只声明输入型形态（embeddings_json）的实例按来源连接入选；
//   - 来源连接同时含对话生成形态时实例必须被 NOT EXISTS 排除；
//   - 非实例账户仍按自身连接判定（CASE 不改变 owner 行为）。
//
// 候选 SQL、proberepo.LoadAccountForTest/LoadAccountForGroup、探针规格解析与
// 健康事实写回都走生产实现；只有上游 HTTP 探测以记录型 fake 替换。外部 PG
// 门禁沿用 pgtestkit（JUHE_AI_TEST_EXTERNAL=1，日常回归默认跳过）。

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountprobe"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountquality"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/proberepo"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

const (
	connectivityPGSecret = "connectivity-health-pg-secret"
	connectivityPGNow    = "2026-11-21T10:00:00.000Z"
)

var connectivityPGNowTime = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

// connectivityPGSchema 是候选 SQL + proberepo 读取链消费的最小列集（生产
// juhe_business schema 的读面投影；不建 FK，seed 顺序不受生产外键链约束）。
const connectivityPGSchema = `
CREATE SCHEMA IF NOT EXISTS juhe_business;
CREATE TABLE IF NOT EXISTS juhe_business.accounts (
  id text PRIMARY KEY,
  system_account_id text NOT NULL,
  name text NOT NULL,
  type text NOT NULL,
  status text NOT NULL DEFAULT 'active',
  schedulable integer NOT NULL DEFAULT 1,
  vendor_code text,
  oauth_type text,
  connection_driver_code text NOT NULL DEFAULT '',
  protocol_code text NOT NULL DEFAULT '',
  protocol_version text NOT NULL DEFAULT '',
  credentials_encrypted text NOT NULL,
  proxy_profile_id text,
  proxy_pool_id text,
  account_expires_at text,
  cooldown_until text,
  last_error_code text,
  last_error_message text,
  last_error_trace_id text,
  config_revision integer NOT NULL DEFAULT 1,
  dispatch_revision bigint NOT NULL DEFAULT 1,
  capability_domain text NOT NULL DEFAULT 'chat',
  health_check_connection_id text,
  health_check_model text,
  health_check_endpoint_mode text,
  background_probe_enabled integer NOT NULL DEFAULT 1,
  availability_schedule_gate integer,
  last_health_check_at text,
  next_health_check_at text,
  last_health_success_at text,
  health_check_failure_count integer NOT NULL DEFAULT 0,
  health_check_failure_started_at text,
  last_health_check_status_code integer,
  last_health_check_error_code text,
  last_health_check_error_message text,
  last_health_check_trace_id text,
  authorization_instance_source_account_id text,
  authorization_instance_authorization_id text,
  authorization_instance_owner_system_account_id text,
  deleted_at text,
  created_at text NOT NULL,
  updated_at text NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_business.account_connections (
  id text PRIMARY KEY,
  account_id text NOT NULL,
  connection_preset_code text NOT NULL,
  base_url text NOT NULL,
  enabled integer NOT NULL DEFAULT 1,
  created_at text NOT NULL,
  updated_at text NOT NULL,
  UNIQUE (account_id, connection_preset_code)
);
CREATE TABLE IF NOT EXISTS juhe_business.account_endpoint_modes (
  connection_id text NOT NULL,
  endpoint_mode text NOT NULL,
  PRIMARY KEY (connection_id, endpoint_mode)
);
CREATE TABLE IF NOT EXISTS juhe_business.groups (
  id text PRIMARY KEY,
  system_account_id text NOT NULL,
  name text NOT NULL,
  enabled integer NOT NULL DEFAULT 1,
  created_at text NOT NULL,
  updated_at text NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_business.group_accounts (
  system_account_id text NOT NULL,
  group_id text NOT NULL,
  account_id text NOT NULL,
  enabled integer NOT NULL DEFAULT 1,
  created_at text NOT NULL,
  updated_at text NOT NULL,
  PRIMARY KEY (group_id, account_id)
);
CREATE TABLE IF NOT EXISTS juhe_business.resource_authorizations (
  id text PRIMARY KEY,
  resource_type text NOT NULL,
  resource_id text NOT NULL,
  resource_owner_system_account_id text NOT NULL,
  grantee_system_account_id text NOT NULL,
  status text NOT NULL DEFAULT 'active',
  expires_at text,
  created_at text NOT NULL,
  updated_at text NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_business.models (
  id text PRIMARY KEY,
  vendor_code text,
  public_model text NOT NULL,
  upstream_model text NOT NULL,
  scope text NOT NULL,
  status text NOT NULL DEFAULT 'active',
  created_at text NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_business.account_supported_models (
  account_id text NOT NULL,
  model_id text NOT NULL,
  created_at text NOT NULL,
  PRIMARY KEY (account_id, model_id)
);
CREATE TABLE IF NOT EXISTS juhe_business.account_api_key_runtime_states (
  id text PRIMARY KEY,
  system_account_id text NOT NULL,
  account_id text NOT NULL,
  key_fingerprint text NOT NULL,
  key_index integer NOT NULL DEFAULT 0,
  status text NOT NULL DEFAULT 'active',
  created_at text NOT NULL,
  updated_at text NOT NULL,
  UNIQUE (account_id, key_fingerprint)
);
CREATE TABLE IF NOT EXISTS juhe_business.proxy_profiles (
  id text PRIMARY KEY,
  enabled boolean NOT NULL DEFAULT true,
  pool_health_status text,
  pool_health_failure_streak integer NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS juhe_business.proxy_pools (
  id text PRIMARY KEY,
  enabled boolean NOT NULL DEFAULT true
);
CREATE TABLE IF NOT EXISTS juhe_business.account_proxy_leases (
  account_id text NOT NULL,
  pool_id text NOT NULL,
  proxy_profile_id text NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_business.proxy_pool_members (
  pool_id text NOT NULL,
  proxy_profile_id text NOT NULL,
  enabled boolean NOT NULL DEFAULT true
);
CREATE TABLE IF NOT EXISTS juhe_business.system_settings (
  system_account_id text NOT NULL,
  key text NOT NULL,
  value_json text NOT NULL,
  PRIMARY KEY (system_account_id, key)
)`

// connectivityProbeRecorder 记录实际执行的探测视图；Runner 并发下加锁。
type connectivityProbeRecorder struct {
	mu    sync.Mutex
	views map[string]*accountprobe.View
}

func (p *connectivityProbeRecorder) ManualConnectivityDiagnostics(_ context.Context, view *accountprobe.View, _ bool) (*accountquality.ProbeObservation, []accountprobe.PoolKeyAttempt, error) {
	p.mu.Lock()
	if p.views == nil {
		p.views = map[string]*accountprobe.View{}
	}
	p.views[view.AccountID] = view
	p.mu.Unlock()
	status := 200
	return &accountquality.ProbeObservation{
		Result:   accountquality.ProbeResult{Success: true, StatusCode: &status},
		Evidence: accountquality.ProbeEvidence{Connectivity: true},
	}, nil, nil
}

func (p *connectivityProbeRecorder) view(accountID string) *accountprobe.View {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.views[accountID]
}

func connectivityPGExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("seed %s: %v", query, err)
	}
}

func connectivityPGSeal(t *testing.T, credentials map[string]any) string {
	t.Helper()
	envelope, err := accountcrypto.EncryptJSON(connectivityPGSecret, credentials)
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

// connectivityPGSeedConnection 写入账户探针连接并声明 modes。
func connectivityPGSeedConnection(t *testing.T, db *sql.DB, accountID, preset, baseURL string, modes ...string) {
	t.Helper()
	connectionID := "conn-" + accountID
	connectivityPGExec(t, db, `INSERT INTO juhe_business.account_connections
		(id, account_id, connection_preset_code, base_url, enabled, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 1, $5, $5)`, connectionID, accountID, preset, baseURL, connectivityPGNow)
	for _, mode := range modes {
		connectivityPGExec(t, db, `INSERT INTO juhe_business.account_endpoint_modes (connection_id, endpoint_mode)
			VALUES ($1, $2)`, connectionID, mode)
	}
	connectivityPGExec(t, db, `UPDATE juhe_business.accounts SET health_check_connection_id = $1 WHERE id = $2`,
		connectionID, accountID)
}

// connectivityPGSeedBinding 写入接收方自有分组与实例绑定（runner 取实例行
// system_account_id 作为访问身份，BoundGroupID 也按该身份解析）。
func connectivityPGSeedBinding(t *testing.T, db *sql.DB, groupID, systemAccountID, accountID string) {
	t.Helper()
	connectivityPGExec(t, db, `INSERT INTO juhe_business.groups
		(id, system_account_id, name, enabled, created_at, updated_at)
		VALUES ($1, $2, 'g', 1, $3, $3)`, groupID, systemAccountID, connectivityPGNow)
	connectivityPGExec(t, db, `INSERT INTO juhe_business.group_accounts
		(system_account_id, group_id, account_id, enabled, created_at, updated_at)
		VALUES ($1, $2, $3, 1, $4, $4)`, systemAccountID, groupID, accountID, connectivityPGNow)
}

func TestConnectivityCandidateAuthorizedInstancesPG(t *testing.T) {
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, connectivityPGSchema)
		return err
	})
	ctx := context.Background()

	// 来源账户一律排在候选扫描窗口之外（未来 next_health_check_at），
	// 只让实例与 owner 对照账户进入本轮候选。
	const sourceNextDue = "2026-11-22T10:00:00.000Z"
	seedSource := func(t *testing.T, id, domain, healthModel, healthMode string, credentials map[string]any) {
		t.Helper()
		connectivityPGExec(t, db, `INSERT INTO juhe_business.accounts
			(id, system_account_id, name, type, status, vendor_code, connection_driver_code, protocol_code, protocol_version,
			 credentials_encrypted, capability_domain, health_check_model, health_check_endpoint_mode,
			 next_health_check_at, created_at, updated_at)
			VALUES ($1, 'sys-owner', $1, 'api_key', 'active', 'gpt', 'gpt', 'openai', 'v1',
			 $2, $3, $4, $5, $6, $7, $7)`,
			id, connectivityPGSeal(t, credentials), domain, healthModel, healthMode, sourceNextDue, connectivityPGNow)
	}
	seedInstance := func(t *testing.T, id, sourceID, authorizationID, domain string) {
		t.Helper()
		connectivityPGExec(t, db, `INSERT INTO juhe_business.accounts
			(id, system_account_id, name, type, status, credentials_encrypted, connection_driver_code,
			 protocol_code, protocol_version, capability_domain,
			 authorization_instance_authorization_id, authorization_instance_source_account_id,
			 authorization_instance_owner_system_account_id, created_at, updated_at)
			VALUES ($1, 'sys-grantee', $1, 'api_key', 'active', '{}', 'gpt', 'openai', 'v1', $2, $3, $4, 'sys-owner', $5, $5)`,
			id, domain, authorizationID, sourceID, connectivityPGNow)
		connectivityPGExec(t, db, `INSERT INTO juhe_business.resource_authorizations
			(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, status, created_at, updated_at)
			VALUES ($1, 'account', $2, 'sys-owner', 'sys-grantee', 'active', $3, $3)`,
			authorizationID, sourceID, connectivityPGNow)
	}

	// 1) 媒体域实例：自身无连接行，来源连接声明 images_json。
	seedSource(t, "src-media", "image", "gpt-image-model", "images_json",
		map[string]any{"api_keys": []any{"sk-media"}})
	connectivityPGSeedConnection(t, db, "src-media", "gpt_openai_v1", "https://media.example.com/v1", "images_json")
	seedInstance(t, "inst-media", "src-media", "authz-media", "image")
	connectivityPGSeedBinding(t, db, "group-media", "sys-grantee", "inst-media")

	// 2) chat 域仅输入型实例：来源连接只声明 embeddings_json。
	seedSource(t, "src-embed", "chat", "gpt-embed-model", "embeddings_json",
		map[string]any{"api_keys": []any{"sk-embed"}})
	connectivityPGSeedConnection(t, db, "src-embed", "gpt_openai_v1", "https://embed.example.com/v1", "embeddings_json")
	seedInstance(t, "inst-embed", "src-embed", "authz-embed", "chat")
	connectivityPGSeedBinding(t, db, "group-embed", "sys-grantee", "inst-embed")

	// 3) chat 域实例的来源连接同时含对话生成形态：必须被 NOT EXISTS 排除。
	seedSource(t, "src-chatgen", "chat", "gpt-chat-model", "chat_json",
		map[string]any{"api_keys": []any{"sk-chatgen"}})
	connectivityPGSeedConnection(t, db, "src-chatgen", "gpt_openai_v1", "https://chatgen.example.com/v1", "embeddings_json", "chat_json")
	seedInstance(t, "inst-chatgen", "src-chatgen", "authz-chatgen", "chat")
	connectivityPGSeedBinding(t, db, "group-chatgen", "sys-grantee", "inst-chatgen")

	// 4) 非实例对照：自身连接只声明输入型形态，必须继续入选。
	connectivityPGExec(t, db, `INSERT INTO juhe_business.accounts
		(id, system_account_id, name, type, status, vendor_code, connection_driver_code, protocol_code, protocol_version,
		 credentials_encrypted, capability_domain, health_check_model, health_check_endpoint_mode, created_at, updated_at)
		VALUES ('plain-embed', 'sys-grantee', 'owner', 'api_key', 'active', 'gpt', 'gpt', 'openai', 'v1',
		 $1, 'chat', 'gpt-embed-model', 'embeddings_json', $2, $2)`,
		connectivityPGSeal(t, map[string]any{"api_keys": []any{"sk-plain"}}), connectivityPGNow)
	connectivityPGSeedConnection(t, db, "plain-embed", "gpt_openai_v1", "https://plain.example.com/v1", "embeddings_json")
	connectivityPGSeedBinding(t, db, "group-plain", "sys-grantee", "plain-embed")

	// 实例自身没有连接行（bug 前提）。
	var ownConnections int
	if err := db.QueryRow(`SELECT COUNT(*) FROM juhe_business.account_connections WHERE account_id IN ('inst-media', 'inst-embed')`).Scan(&ownConnections); err != nil {
		t.Fatal(err)
	}
	if ownConnections != 0 {
		t.Fatalf("实例不得拥有自身连接行，got %d", ownConnections)
	}

	store, err := proberepo.NewStore(proberepo.Config{DB: db, Secret: connectivityPGSecret, Now: func() time.Time { return connectivityPGNowTime }})
	if err != nil {
		t.Fatal(err)
	}
	prober := &connectivityProbeRecorder{}
	// Concurrency 必须覆盖候选全集：clampConnectivityBudget 会把批量收敛到
	// 并发（单波预算），并发 1 时 LIMIT 1 只扫首个候选。
	runner, err := NewRunner(Config{
		DB:          db,
		Reader:      store,
		Prober:      prober,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:         func() time.Time { return connectivityPGNowTime },
		Concurrency: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Scanned != 3 || result.Probed != 3 || result.Succeeded != 3 {
		t.Fatalf("候选/探测结果 = %+v（应只选中 inst-media/inst-embed/plain-embed）", result)
	}

	for _, accountID := range []string{"inst-media", "inst-embed", "plain-embed"} {
		if prober.view(accountID) == nil {
			t.Fatalf("%s 必须完成探测", accountID)
		}
	}
	if prober.view("inst-chatgen") != nil {
		t.Fatal("来源连接含对话生成形态的实例必须被排除")
	}

	media := prober.view("inst-media")
	if media.BaseURL != "https://media.example.com/v1" || media.ConnectionPresetCode != "gpt_openai_v1" {
		t.Fatalf("媒体实例必须使用来源连接: %+v", media)
	}
	if media.SelectedAPIKey != "sk-media" || !media.NormalizeEndpointModes[accountprobe.ModeImagesJSON] {
		t.Fatalf("媒体实例必须使用来源凭据/连接形态: key=%q modes=%v", media.SelectedAPIKey, media.NormalizeEndpointModes)
	}
	embed := prober.view("inst-embed")
	if embed.BaseURL != "https://embed.example.com/v1" || embed.SelectedAPIKey != "sk-embed" {
		t.Fatalf("chat 仅输入型实例必须使用来源连接/凭据: %+v", embed)
	}
	if !embed.NormalizeEndpointModes[accountprobe.ModeEmbeddingsJSON] || len(embed.NormalizeEndpointModes) != 1 {
		t.Fatalf("chat 仅输入型实例形态 = %v", embed.NormalizeEndpointModes)
	}
	plain := prober.view("plain-embed")
	if plain.BaseURL != "https://plain.example.com/v1" || plain.SelectedAPIKey != "sk-plain" {
		t.Fatalf("非实例账户必须继续使用自身连接/凭据: %+v", plain)
	}

	// 健康事实：入选账户推进 health check；被排除实例保持未检查。
	for _, accountID := range []string{"inst-media", "inst-embed", "plain-embed"} {
		var lastChecked sql.NullString
		var nextDue sql.NullString
		var status string
		if err := db.QueryRow(`SELECT last_health_check_at, next_health_check_at, status FROM juhe_business.accounts WHERE id = $1`, accountID).
			Scan(&lastChecked, &nextDue, &status); err != nil {
			t.Fatal(err)
		}
		if !lastChecked.Valid || lastChecked.String == "" || !nextDue.Valid || nextDue.String <= connectivityPGNow {
			t.Fatalf("%s 健康事实未推进: last=%v next=%v", accountID, lastChecked, nextDue)
		}
		if status != "active" {
			t.Fatalf("%s 成功探测后状态 = %q", accountID, status)
		}
	}
	var excludedLast sql.NullString
	if err := db.QueryRow(`SELECT last_health_check_at FROM juhe_business.accounts WHERE id = 'inst-chatgen'`).Scan(&excludedLast); err != nil {
		t.Fatal(err)
	}
	if excludedLast.Valid && excludedLast.String != "" {
		t.Fatalf("被排除实例不得写健康事实: %q", excludedLast.String)
	}
}

// TestConnectivityClampedBatchCarriesOverDuePG 钉住 clamp 预算语义：批量收敛到
// 并发（单波 ≤ min(batch, concurrency)）后，缩批不丢候选——本轮未扫到的到期
// 账户保持 due，下一 tick（同一 Run 入口）继续被选出。
func TestConnectivityClampedBatchCarriesOverDuePG(t *testing.T) {
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, connectivityPGSchema)
		return err
	})
	ctx := context.Background()

	seedPlain := func(t *testing.T, id string) {
		t.Helper()
		connectivityPGExec(t, db, `INSERT INTO juhe_business.accounts
			(id, system_account_id, name, type, status, vendor_code, connection_driver_code, protocol_code, protocol_version,
			 credentials_encrypted, capability_domain, health_check_model, health_check_endpoint_mode, created_at, updated_at)
			VALUES ($1, 'sys-grantee', $1, 'api_key', 'active', 'gpt', 'gpt', 'openai', 'v1',
			 $2, 'image', 'gpt-image-model', 'images_json', $3, $3)`,
			id, connectivityPGSeal(t, map[string]any{"api_keys": []any{"sk-" + id}}), connectivityPGNow)
		connectivityPGSeedConnection(t, db, id, "gpt_openai_v1", "https://"+id+".example.com/v1", "images_json")
		connectivityPGSeedBinding(t, db, "group-"+id, "sys-grantee", id)
	}
	seedPlain(t, "clamp-a")
	seedPlain(t, "clamp-b")

	newRunner := func(t *testing.T, prober *connectivityProbeRecorder) *Runner {
		t.Helper()
		store, err := proberepo.NewStore(proberepo.Config{DB: db, Secret: connectivityPGSecret, Now: func() time.Time { return connectivityPGNowTime }})
		if err != nil {
			t.Fatal(err)
		}
		// Concurrency:1 → clamp 批量收敛为 1，每次 Run 只扫一个到期候选。
		runner, err := NewRunner(Config{
			DB:          db,
			Reader:      store,
			Prober:      prober,
			Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
			Now:         func() time.Time { return connectivityPGNowTime },
			Concurrency: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		return runner
	}

	proberA := &connectivityProbeRecorder{}
	resultA, err := newRunner(t, proberA).Run(ctx)
	if err != nil {
		t.Fatalf("run1: %v", err)
	}
	if resultA.Scanned != 1 || resultA.Probed != 1 || resultA.Succeeded != 1 {
		t.Fatalf("clamp 后首轮 = %+v（批量应收敛为 1）", resultA)
	}
	if proberA.view("clamp-a") == nil && proberA.view("clamp-b") == nil {
		t.Fatal("首轮必须探测一个候选")
	}
	if proberA.view("clamp-a") != nil && proberA.view("clamp-b") != nil {
		t.Fatal("批量收敛为 1 时不得同轮探测两个候选")
	}

	// 第二 tick：已探测账户 next_health_check_at 已推进出窗口，剩余候选继续入选。
	proberB := &connectivityProbeRecorder{}
	resultB, err := newRunner(t, proberB).Run(ctx)
	if err != nil {
		t.Fatalf("run2: %v", err)
	}
	if resultB.Scanned != 1 || resultB.Probed != 1 || resultB.Succeeded != 1 {
		t.Fatalf("clamp 后次轮 = %+v（剩余候选应继续入选）", resultB)
	}
	// 两个 recorder 分别只有本轮探测的账户；跨两轮合并必须覆盖两个候选，
	// 且次轮探测的是首轮未扫到的那个。
	if proberA.view("clamp-a") == nil && proberB.view("clamp-a") == nil {
		t.Fatal("clamp-a 跨 tick 未被探测")
	}
	if proberA.view("clamp-b") == nil && proberB.view("clamp-b") == nil {
		t.Fatal("clamp-b 跨 tick 未被探测")
	}
	firstProbed, secondProbed := "clamp-b", "clamp-a"
	if proberA.view("clamp-a") != nil {
		firstProbed, secondProbed = "clamp-a", "clamp-b"
	}
	if proberA.view(secondProbed) != nil || proberB.view(firstProbed) != nil {
		t.Fatalf("缩批不得重复探测: first=%s second=%s", firstProbed, secondProbed)
	}
}
