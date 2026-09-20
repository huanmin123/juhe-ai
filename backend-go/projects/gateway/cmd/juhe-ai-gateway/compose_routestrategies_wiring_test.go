package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

// TestGatewayAPIKeyValidationBusInvalidatorPublishesRouteStrategyReason 钉住
// M06 validation-cache 失效适配器的发布面：reason 原样落
// topic:gateway_api_key_validation_cache（无 apiKeyId 后缀 → 订阅方
// gatewayruntimecache 做无差别全清），nil bus 保持 no-op 分支。
func TestGatewayAPIKeyValidationBusInvalidatorPublishesRouteStrategyReason(t *testing.T) {
	bus := inval.New(time.Now)
	reasons := map[string][]string{}
	stop := bus.Subscribe(inval.TopicGatewayAPIKeyValidation, func(_ string, reason string) {
		reasons[inval.TopicGatewayAPIKeyValidation] = append(reasons[inval.TopicGatewayAPIKeyValidation], reason)
	})
	defer stop()

	invalidator := gatewayAPIKeyValidationBusInvalidator{bus: bus}
	if err := invalidator.InvalidateValidationCache("route_strategy_updated"); err != nil {
		t.Fatalf("InvalidateValidationCache: %v", err)
	}
	got := reasons[inval.TopicGatewayAPIKeyValidation]
	if len(got) != 1 || got[0] != "route_strategy_updated" {
		t.Fatalf("published reasons=%#v, want [route_strategy_updated]", got)
	}

	if err := (gatewayAPIKeyValidationBusInvalidator{}).InvalidateValidationCache("route_strategy_updated"); err != nil {
		t.Fatalf("nil bus invalidation must stay a no-op success: %v", err)
	}
}

// TestRouteStrategySpeedFirstFacadeMapsAndDegrades 钉住 facade 适配器与
// gatewayproxyhealth.LatencyDegradationService 的形态：空存储 available=true、
// 超 50 策略路由的存储错误映射为 available=false（Node 的
// runtimeAvailable:false 渲染）、空白 id 清理为 0。
func TestRouteStrategySpeedFirstFacadeMapsAndDegrades(t *testing.T) {
	service := gatewayproxyhealth.NewLatencyDegradationService(
		gatewayproxyhealth.NewMemoryRuntimeStateStore(nil), nil, gatewayproxyhealth.LatencyDegradationOptions{})
	facade := routeStrategySpeedFirstFacade{service: service}

	items, available, err := facade.ListDegradedRuntime(context.Background(), nil, []string{"rs_1"})
	if err != nil || !available {
		t.Fatalf("empty memory store: available=%v err=%v", available, err)
	}
	if len(items) != 0 {
		t.Fatalf("empty memory store items=%#v", items)
	}

	overflow := make([]string, 0, 51)
	for i := 0; i < 51; i++ {
		overflow = append(overflow, fmt.Sprintf("rs_%03d", i))
	}
	if _, available, err = facade.ListDegradedRuntime(context.Background(), nil, overflow); err == nil || available {
		t.Fatalf("overflow ids must degrade to unavailable: available=%v err=%v", available, err)
	}

	cleared, err := facade.ClearDegradedRuntime(context.Background(), "  ")
	if err != nil || cleared != 0 {
		t.Fatalf("blank clear: cleared=%d err=%v", cleared, err)
	}
}

// routestrategiesWiringEnv mirrors the routestrategies package test env
// (bare in-memory sqlite + authsys deps, no schema: the probe never reaches a
// query — unauthenticated requests stop at the session middleware).
func routestrategiesWiringEnv(t *testing.T) (*authsys.Deps, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:routestrategies-wiring-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	service, err := businessauth.New(db, modelcheckauth.SQLite, time.Now, businessauth.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := authsys.NewAccountStore(db, modelcheckauth.SQLite, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &authsys.Deps{
		Port: service, Accounts: accounts, Captcha: modelcheckauth.NewCaptchaService(nil),
		LoginGuard: modelcheckauth.NewLoginGuard(nil), CaptchaDisabled: true,
	}, db
}

// TestRoutestrategiesMountRegistersSpeedFirstEndpointsWhenFacadeWired 复验组合
// 根的注册判据（routes.go Mount 只在 facade 存在时注册 speed-first-runtime）：
// SetSpeedFirstRuntimeFacade 在 Deps.Mount 之前调用 → 端点已注册（未登录 401
// 请先登录），facade 缺席 → 端点未挂载（kernel 未匹配 404）。compose.go 中
// setter 与 Mount 相邻且在链条装配之后，本测试用真实 Deps 复现同一顺序判据。
func TestRoutestrategiesMountRegistersSpeedFirstEndpointsWhenFacadeWired(t *testing.T) {
	service := gatewayproxyhealth.NewLatencyDegradationService(
		gatewayproxyhealth.NewMemoryRuntimeStateStore(nil), nil, gatewayproxyhealth.LatencyDegradationOptions{})
	authDeps, db := routestrategiesWiringEnv(t)

	for name, wired := range map[string]bool{"facade_wired": true, "facade_absent": false} {
		t.Run(name, func(t *testing.T) {
			store, err := routestrategies.NewStore(db, false, nil, nil, nil)
			if err != nil {
				t.Fatalf("new store: %v", err)
			}
			if wired {
				store.SetSpeedFirstRuntimeFacade(routeStrategySpeedFirstFacade{service: service})
			}
			k := kernel.New(kernel.Options{SystemAPIPrefix: "/__aisys__"})
			(&routestrategies.Deps{Store: store, Auth: authDeps}).Mount(k)
			server := httptest.NewServer(k.Handler())
			t.Cleanup(server.Close)

			response, err := http.Get(server.URL + "/__aisys__/api/route-strategies/rs_probe/speed-first-runtime")
			if err != nil {
				t.Fatalf("GET speed-first-runtime: %v", err)
			}
			defer response.Body.Close()
			if wired && response.StatusCode != http.StatusUnauthorized {
				t.Fatalf("wired facade must register the endpoint (unauthenticated 401), got %d", response.StatusCode)
			}
			if !wired && response.StatusCode != http.StatusNotFound {
				t.Fatalf("absent facade must keep the endpoint unmounted (404), got %d", response.StatusCode)
			}
		})
	}
}
