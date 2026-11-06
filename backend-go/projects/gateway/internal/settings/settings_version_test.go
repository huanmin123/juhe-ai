package settings

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
)

type fakeSettingsVersionBumper struct {
	calls int
	err   error
}

func (f *fakeSettingsVersionBumper) BumpSettingsVersion(context.Context) error {
	f.calls++
	return f.err
}

// B5：system_settings 事务提交后 bump 一次；校验失败/DB 失败不 bump；bump
// 失败不回滚已提交写。
func TestSettingsUpdateBumpsVersion(t *testing.T) {
	env := newTestEnv(t)
	bumper := &fakeSettingsVersionBumper{}
	env.store.SetVersionBumper(bumper)

	if _, err := env.store.Update(context.Background(), map[string]any{
		"gatewayTextRawBodyLimitMegabytes": 32,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if bumper.calls != 1 {
		t.Fatalf("bump calls=%d, want 1", bumper.calls)
	}

	if _, err := env.store.Update(context.Background(), map[string]any{"not-a-setting": 1}); err == nil {
		t.Fatal("invalid key must fail")
	}
	if bumper.calls != 1 {
		t.Fatalf("invalid update bumped: calls=%d", bumper.calls)
	}

	bumper.err = errors.New("redis down")
	if _, err := env.store.Update(context.Background(), map[string]any{
		"gatewayTextRawBodyLimitMegabytes": 33,
	}); err != nil {
		t.Fatalf("bump failure must not fail Update: %v", err)
	}
	if bumper.calls != 2 {
		t.Fatalf("bump failure calls=%d, want 2", bumper.calls)
	}

	// 分区写路径（system 域）同样 bump；global 域的 brand 仍走 60s 进程缓存，
	// 不接入跨进程版本序列。
	bumper.err = nil
	if _, err := env.store.UpdateSection(context.Background(), "gateway-core", map[string]any{
		"gatewayTextRawBodyLimitMegabytes": 34,
	}); err != nil {
		t.Fatalf("UpdateSection: %v", err)
	}
	if bumper.calls != 3 {
		t.Fatalf("system section update calls=%d, want 3", bumper.calls)
	}
	if _, err := env.store.UpdateSection(context.Background(), "brand", map[string]any{
		"appName": "跨进程测试",
	}); err != nil {
		t.Fatalf("brand UpdateSection: %v", err)
	}
	if bumper.calls != 3 {
		t.Fatalf("global section bumped: calls=%d", bumper.calls)
	}
}

// B5：peer 收到跨实例 settings 失效后必须重放 upstream 手动覆盖，关闭
// “只在启动应用一次”的无界滞后。
func TestRefreshUpstreamClientVersionOverridesAppliesPeerChange(t *testing.T) {
	env := newTestEnv(t)
	t.Cleanup(func() { upstreamidentity.SetClientVersionOverrides(nil) })
	now := "2026-09-04T00:00:00.000Z"
	if _, err := env.db.Exec(fixtureSQL(`INSERT INTO system_settings (system_account_id, key, value_json, updated_at) VALUES ('sys_admin', 'upstreamClientVersionOverrides', ?, ?) ON CONFLICT (system_account_id, key) DO UPDATE SET value_json = EXCLUDED.value_json`),
		`{"codex":"9.9.9"}`, now); err != nil {
		t.Fatal(err)
	}
	if err := env.store.RefreshUpstreamClientVersionOverrides(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := upstreamidentity.EffectiveCodexVersion(); got != "9.9.9" {
		t.Fatalf("effective codex version=%q, want 9.9.9", got)
	}
}

// B5：跨实例失效回调走异步重放，运行期间的新触发不能被 TryLock 丢弃；
// 最终必须收敛到最新 PG 值。
func TestScheduleUpstreamClientVersionOverridesRefreshConverges(t *testing.T) {
	env := newTestEnv(t)
	t.Cleanup(func() { upstreamidentity.SetClientVersionOverrides(nil) })
	now := "2026-09-04T00:00:00.000Z"
	upsert := func(version string) {
		t.Helper()
		if _, err := env.db.Exec(fixtureSQL(`INSERT INTO system_settings (system_account_id, key, value_json, updated_at) VALUES ('sys_admin', 'upstreamClientVersionOverrides', ?, ?) ON CONFLICT (system_account_id, key) DO UPDATE SET value_json = EXCLUDED.value_json`),
			`{"codex":"`+version+`"}`, now); err != nil {
			t.Fatal(err)
		}
	}
	upsert("9.9.9")
	env.store.ScheduleUpstreamClientVersionOverridesRefresh()
	upsert("8.8.8")
	env.store.ScheduleUpstreamClientVersionOverridesRefresh()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if upstreamidentity.EffectiveCodexVersion() == "8.8.8" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("async refresh did not converge to latest value: %q", upstreamidentity.EffectiveCodexVersion())
}
