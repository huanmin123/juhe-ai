package circuitstore

// 投影 LoadItems 单元测试：SQLite 双模全链（seed → LoadItems → ProjectionItem
// 断言）+ 状态机分类纯函数。运行态源以 Mock 注入（fail-closed 与分类分支
// 可回放）。
// 组B保留（不迁 PG）：loader 的 PG 臂与生产 schema 存在适配缺口（timestamptz
// 列裸扫 text 断言等，取证见 final_edges_test.go TestHydratePageFailClosed
// 注释），随 loader PG 臂修复批次再迁。

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

type stubCredentials struct{}

func (stubCredentials) DecryptCredentials(envelope string) (map[string]any, error) {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(envelope), &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func (stubCredentials) AccountAPIKeyEntries(credentials map[string]any) []APIKeyPoolEntry {
	return []APIKeyPoolEntry{}
}

type stubConcurrency struct {
	values map[string]int
	err    error
}

func (s stubConcurrency) LoadConcurrency(ctx context.Context, accountIDs []string) (map[string]int, error) {
	if s.err != nil {
		return nil, s.err
	}
	output := map[string]int{}
	for _, id := range accountIDs {
		output[id] = s.values[id]
	}
	return output, nil
}

type stubRuntime struct {
	values map[string]AccountRuntimeAvailability
	err    error
}

func (s stubRuntime) LoadRuntimeAvailability(ctx context.Context, runtimeKeys []string) (map[string]AccountRuntimeAvailability, error) {
	if s.err != nil {
		return nil, s.err
	}
	output := map[string]AccountRuntimeAvailability{}
	for _, key := range runtimeKeys {
		if value, found := s.values[key]; found {
			output[key] = value
		}
	}
	return output, nil
}

type stubTimezone struct{}

func (stubTimezone) StatsTimezone(ctx context.Context) (*time.Location, error) {
	return time.UTC, nil
}

func newTestLoader(business *sql.DB, concurrency ConcurrencySource, runtime RuntimeAvailabilitySource) *ProjectionItemLoader {
	loader, err := NewProjectionItemLoader(ProjectionLoadConfig{
		Business:            business,
		Stats:               business,
		Secret:              "test-secret",
		Credentials:         stubCredentials{},
		Concurrency:         concurrency,
		RuntimeAvailability: runtime,
		Timezone:            stubTimezone{},
		Now:                 func() time.Time { return time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		panic(err)
	}
	return loader
}

const loaderNow = "2026-09-04T10:00:00.000Z"
const loaderFuture = "2026-09-05T10:00:00.000Z"

func seedOwnerAccount(t *testing.T, db *sql.DB) {
	t.Helper()
	exec := func(query string, args ...any) {
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, query)
		}
	}
	exec(`INSERT INTO system_accounts (id, username, display_name) VALUES ('sys-1', 'admin', 'Admin')`)
	exec(`INSERT INTO vendors (code, name) VALUES ('openai', 'OpenAI')`)
	exec(`INSERT INTO accounts (
			id, config_revision, system_account_id, vendor_code,
			protocol_code, protocol_version, name, type, status, schedulable,
			concurrency_limit, priority, super_priority_enabled, fallback_enabled,
			health_check_model, health_check_endpoint_mode, created_at, updated_at
		) VALUES (
			'acct-1', 3, 'sys-1', 'openai',
			'openai', 'v1', '主账户', 'api_key', 'active', 1,
			10, 5, 1, 0,
			'gpt-4o-mini', 'chat_completions', '2026-01-01T00:00:00.000Z', '2026-01-02T00:00:00.000Z')`)
}

func TestAccountFilterStatusesClassification(t *testing.T) {
	available := effectiveAvailability{available: true, status: "available"}
	if got := accountFilterStatuses("active", &available, false); len(got) != 1 || got[0] != "active" {
		t.Fatalf("active+available 应分类为 active: %v", got)
	}
	cooldown := blockedAvailability("instance_cooldown", "", "", "", "", "")
	if got := accountFilterStatuses("active", &cooldown, false); len(got) != 1 || got[0] != "temporary_unavailable" {
		t.Fatalf("instance_cooldown 应分类为 temporary_unavailable: %v", got)
	}
	// derivedStatus 缺失（无派生状态）时 quota exceeded 生效（Node 同序）。
	if got := accountFilterStatuses("active", nil, true); len(got) != 1 || got[0] != "rate_limited" {
		t.Fatalf("quota exceeded（无派生状态）应分类为 rate_limited: %v", got)
	}
	degraded := effectiveAvailability{available: true, status: "runtime_degraded"}
	if got := accountFilterStatuses("active", &degraded, false); len(got) != 1 || got[0] != "active" {
		t.Fatalf("runtime_degraded 应分类为 active: %v", got)
	}
	unschedulable := blockedAvailability("instance_unschedulable", "", "", "", "", "")
	if got := accountFilterStatuses("active", &unschedulable, false); len(got) != 1 || got[0] != "disabled" {
		t.Fatalf("instance_unschedulable 应分类为 disabled: %v", got)
	}
	// status=active 且不可用且无派生状态：空集合 → 调用方抛错。
	unknownBlocked := effectiveAvailability{available: false, status: ""}
	if got := accountFilterStatuses("active", &unknownBlocked, false); len(got) != 0 {
		t.Fatalf("active+不可用+无派生应返回空集合: %v", got)
	}
}

func TestSchedulableBucketWithEffectiveAvailable(t *testing.T) {
	if opsjobs.SchedulableBucket("disabled", false) != "disabled" {
		t.Fatalf("disabled+不可用应为 disabled 桶")
	}
	if opsjobs.SchedulableBucket("disabled", true) != "enabled" {
		t.Fatalf("disabled+可用应为 enabled 桶")
	}
	if opsjobs.SchedulableBucket("rate_limited", true) != "cooling" {
		t.Fatalf("rate_limited 恒为 cooling 桶")
	}
}

func payloadValue[T any](t *testing.T, payload map[string]any, key string) T {
	t.Helper()
	value, ok := payload[key].(T)
	if !ok {
		var zero T
		t.Fatalf("payload[%s] 类型不符: %v (期望 %T)", key, payload[key], zero)
	}
	return value
}
