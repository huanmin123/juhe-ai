package statsagg

// B3-H11 端到端：计划激活重建的 api_key 绑定 + 提交后打脏 → statsagg 增量
// 刷新生成 usage_quota_hourly_windows（gatewayquota.LoadCosts 的小时额度
// 唯一读取源）。绑定/打脏走 jobs internal/quotahourly（排期激活 hook 的同一
// 写侧实现），因此本用例贯通「绑定写侧 → 脏范围消费 → 小时窗口」。

import (
	"context"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/quotahourly"
)

func TestQuotaHourlyWindowFromScheduleActivationBinding(t *testing.T) {
	e := newPGTestEnv(t)
	refresher := e.quotaRefresher(nil)
	timestamp := FormatRFC3339Millis(e.now.Add(-3 * time.Hour))

	upserted, err := quotahourly.SyncAPIKeyBinding(context.Background(), e.db,
		"sa1", "keyA", `{"hourly":{"enabled":true,"hours":6,"limit":12.5}}`, true, timestamp)
	if err != nil {
		t.Fatal(err)
	}
	if !upserted {
		t.Fatal("active + 小时额度必须写入绑定")
	}
	if err := quotahourly.MarkAPIKeyDirty(context.Background(), e.db, "sa1", "keyA", timestamp); err != nil {
		t.Fatal(err)
	}
	// 激活前已有的窗口用量（Key 在窗口内先启用过又停用）：必须立即进入小时窗口。
	e.seedQuotaHourly("sa1", "api_key", "keyA", "2026-04-18T10", 3)

	result := QuotaHourlyWindowRefreshResult{}
	if err := refresher.refreshQuotaHourlyWindowsIncremental(context.Background(), e.zone, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Fatalf("result = %+v, want changed=true", result)
	}
	assertQuotaWindowRows(t, e.quotaWindowRows(t),
		quotaWindowRow{"sa1", "api_key", "keyA", 6, 3},
	)
	if got := e.quotaDirtyCount(t); got != 0 {
		t.Fatalf("dirty scopes left = %d, want 0", got)
	}
}

// TestQuotaHourlyWindowActivationBindingSkipsNonHourly 覆盖停用 / 无小时额度
// 不写绑定：同一写侧在 active=false 或额度中没有启用的小时窗口时返回 false
// 且不落行。
func TestQuotaHourlyWindowActivationBindingSkipsNonHourly(t *testing.T) {
	e := newPGTestEnv(t)
	timestamp := FormatRFC3339Millis(e.now)

	cases := []struct {
		name    string
		keyID   string
		quota   string
		active  bool
		wantErr bool
	}{
		{name: "inactive", keyID: "key-off", quota: `{"hourly":{"enabled":true,"hours":6,"limit":12.5}}`, active: false},
		{name: "no hourly", keyID: "key-daily", quota: `{"daily":{"enabled":true,"limit":40}}`, active: true},
		{name: "no limits", keyID: "key-empty", quota: "", active: true},
	}
	for _, testCase := range cases {
		upserted, err := quotahourly.SyncAPIKeyBinding(context.Background(), e.db,
			"sa1", testCase.keyID, testCase.quota, testCase.active, timestamp)
		if testCase.wantErr {
			if err == nil {
				t.Fatalf("%s: 期望报错", testCase.name)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", testCase.name, err)
		}
		if upserted {
			t.Fatalf("%s: 不应写入绑定", testCase.name)
		}
		var count int
		if err := e.db.QueryRow(`SELECT COUNT(*) FROM juhe_business.request_quota_hourly_window_scope_bindings
			WHERE source_type = 'api_key' AND source_id = $1`, testCase.keyID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s: 绑定行=%d，期望 0", testCase.name, count)
		}
	}
}
