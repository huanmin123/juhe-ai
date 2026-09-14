package main

// w1: compose_account_balance_health.go 与 chain_wiring_w2c.go 收割——
// 余额归属健康探测（可注入 fetch）、分组绑定行双向投影、client-IP 并发
// 槽适配器。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
)

func TestW1AccountBalanceOwnerHealth(t *testing.T) {
	// 非 Go 归属：enabled=false 且直接就绪。
	health := accountBalanceGoOwnerHealth(func(string) string { return "" }, accountBalanceHealthDeps{})
	if health.Enabled || !health.Ready {
		t.Fatalf("non-go owner = %+v", health)
	}
	// Go 归属但缺 endpoint：未就绪且不探测。
	health = accountBalanceGoOwnerHealth(func(key string) string {
		if key == accountBalanceJobsOwnerEnv {
			return "go"
		}
		return ""
	}, accountBalanceHealthDeps{})
	if !health.Enabled || health.Ready {
		t.Fatalf("missing endpoint = %+v", health)
	}
	// standby 模式：探测对端 ownerMode=standby → 就绪；对端 active → 未就绪。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ownerMode":"standby"}`))
	}))
	t.Cleanup(server.Close)
	standbyGetenv := func(key string) string {
		switch key {
		case accountBalanceJobsOwnerEnv:
			return "go"
		case accountBalanceJobsHTTPURLEnv:
			return server.URL
		case ownermode.EnvironmentKey:
			return "standby"
		}
		return ""
	}
	health = accountBalanceGoOwnerHealth(standbyGetenv, accountBalanceHealthDeps{})
	if !health.Ready || health.OwnerMode != "standby" || health.ProjectorReady == nil || !*health.ProjectorReady {
		t.Fatalf("standby health = %+v", health)
	}
	// active 模式：对端三标志齐备才就绪。
	activeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ready":true,"accountBalanceEnabled":true,"accountBalanceReady":true}`))
	}))
	t.Cleanup(activeServer.Close)
	activeGetenv := func(key string) string {
		if key == accountBalanceJobsOwnerEnv {
			return "go"
		}
		if key == accountBalanceJobsHTTPURLEnv {
			return activeServer.URL
		}
		return ""
	}
	health = accountBalanceGoOwnerHealth(activeGetenv, accountBalanceHealthDeps{})
	if !health.Ready {
		t.Fatalf("active health = %+v", health)
	}
	// 对端标志残缺：未就绪。
	partialServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ready":true}`))
	}))
	t.Cleanup(partialServer.Close)
	partialGetenv := func(key string) string {
		if key == accountBalanceJobsHTTPURLEnv {
			return partialServer.URL
		}
		if key == accountBalanceJobsOwnerEnv {
			return "go"
		}
		return ""
	}
	if health := accountBalanceGoOwnerHealth(partialGetenv, accountBalanceHealthDeps{}); health.Ready {
		t.Fatalf("partial peer = %+v", health)
	}
	// 投影器未就绪：不探测直接未就绪。
	notReadyProjector := accountBalanceHealthDeps{ProjectorReady: func() bool { return false }}
	if health := accountBalanceGoOwnerHealth(activeGetenv, notReadyProjector); health.Ready {
		t.Fatal("投影器未就绪必须未就绪")
	}
	// 探测失败（服务关闭）：未就绪。
	closed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closed.Close()
	closedGetenv := func(key string) string {
		if key == accountBalanceJobsHTTPURLEnv {
			return closed.URL
		}
		if key == accountBalanceJobsOwnerEnv {
			return "go"
		}
		return ""
	}
	if health := accountBalanceGoOwnerHealth(closedGetenv, accountBalanceHealthDeps{}); health.Ready {
		t.Fatal("探测失败必须未就绪")
	}
	// 就绪状态投影。
	if got := accountBalanceSystemHealthStatus(true); got == accountBalanceSystemHealthStatus(false) {
		t.Fatal("就绪与未就绪状态必须不同")
	}
}

func TestW1GroupBindingRowProjections(t *testing.T) {
	bindings := []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{{
		ID: "bind_1", APIKeyID: "key_1", SystemAccountID: "sys_1", GroupID: "grp_1",
		Priority: 3, Weight: 7, Status: "active", ProviderCode: "openai", GroupEnabled: 1,
	}}
	projected := chainRoutingBindingRowsOf(bindings)
	if len(projected) != 1 || projected[0].ID != "bind_1" || projected[0].Weight == nil || *projected[0].Weight != 7 {
		t.Fatalf("projected = %+v", projected)
	}
	// 回投：合法权重保留、越界回 1。
	back := chainCacheBindingRowOf(projected[0])
	if back.Weight != 7 || back.Priority != 3 || back.GroupEnabled != 1 {
		t.Fatalf("back = %+v", back)
	}
	over := projected[0]
	overWeight := int64(500)
	over.Weight = &overWeight
	if got := chainCacheBindingRowOf(over); got.Weight != 1 {
		t.Fatalf("over weight = %d", got.Weight)
	}
	nilWeight := projected[0]
	nilWeight.Weight = nil
	if got := chainCacheBindingRowOf(nilWeight); got.Weight != 1 {
		t.Fatalf("nil weight = %d", got.Weight)
	}
}

func TestW1ClientIPConcurrencyAcquire(t *testing.T) {
	slots, err := gatewayclientip.NewClientIPConcurrency(gatewayclientip.ClientIPConcurrencyOptions{})
	adapter := newChainClientIPConcurrency(slots)
	policy := map[string]any{}
	decision, err := adapter.Acquire(context.Background(), gatewaydispatch.ClientIPConcurrencyInput{
		SystemAccountID: "sys_1", GroupID: "grp_1", APIKeyID: "key_1", ClientIP: "203.0.113.5",
		Policy: &policy, Signal: context.Background(),
	})
	if err != nil {
		t.Fatalf("acquire = %v", err)
	}
	if !decision.Acquired {
		t.Fatalf("decision = %+v", decision)
	}
	if decision.Release == nil {
		t.Fatal("获取后必须携带释放闭包")
	}
	decision.Release()
	// nil 策略：同样可获取。
	decision2, err := adapter.Acquire(context.Background(), gatewaydispatch.ClientIPConcurrencyInput{
		SystemAccountID: "sys_1", GroupID: "grp_1", ClientIP: "203.0.113.6", Signal: context.Background(),
	})
	if err != nil || !decision2.Acquired {
		t.Fatalf("nil policy = %+v, %v", decision2, err)
	}
}
