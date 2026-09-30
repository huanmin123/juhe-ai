package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/gometrics"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
)

func TestPassiveGatewayHealthNeverClaimsOwnerReadiness(t *testing.T) {
	record := httptest.NewRecorder()
	passiveGatewayHealthHandler(ownermode.Drain).ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/health", nil))
	if record.Code != http.StatusOK {
		t.Fatalf("health status=%d body=%s", record.Code, record.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(record.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["ready"] != false || payload["ownerReady"] != false || payload["ownerMode"] != "drain" || payload["auditLogReady"] != false {
		t.Fatalf("passive health claimed owner readiness: %#v", payload)
	}
}

func TestLoadSessionRetentionConfigDefaults(t *testing.T) {
	interval, limit, err := loadSessionRetentionConfig(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if interval != 15*time.Minute || limit != 10000 {
		t.Fatalf("defaults interval=%s limit=%d", interval, limit)
	}
}

func TestLoadSessionRetentionConfigContract(t *testing.T) {
	if _, _, err := loadSessionRetentionConfig(func(key string) string {
		if key == "JUHE_AI_SESSION_RETENTION_INTERVAL" {
			return "0s"
		}
		return ""
	}); err == nil {
		t.Fatal("invalid JUHE_AI_SESSION_RETENTION_INTERVAL must fail")
	}
	// 清理批次 C4（2026-09-30）：JUHE_AI_SESSION_RETENTION_BATCH_SIZE 五源
	// 核对零引用，收编为编译期常量默认——残留 env（含非法值）不再参与
	// 解析，也不得再导致启动失败。
	_, limit, err := loadSessionRetentionConfig(func(key string) string {
		if key == "JUHE_AI_SESSION_RETENTION_BATCH_SIZE" {
			return "not-a-number"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("retired BATCH_SIZE env must be ignored: %v", err)
	}
	if limit != 10000 {
		t.Fatalf("batch limit must keep the compiled default 10000: %d", limit)
	}
}

func TestListenLoopbackRejectsPublicAddress(t *testing.T) {
	for _, address := range []string{"0.0.0.0:3306", "192.0.2.10:3306", "invalid"} {
		if listener, err := listenLoopback(address); err == nil {
			_ = listener.Close()
			t.Fatalf("public or invalid address %q must be rejected", address)
		}
	}
}

func TestListenLoopbackPrivateBindRequiresOptIn(t *testing.T) {
	t.Setenv("JUHE_AI_GATEWAY_HEALTH_ALLOW_NON_LOOPBACK", "")
	for _, host := range []string{"0.0.0.0", "10.42.0.5", "192.0.2.10"} {
		if err := validateHealthListenHost(host); err == nil {
			t.Fatalf("未开启放行开关时非回环地址必须拒绝: %s", host)
		}
	}
	if err := validateHealthListenHost("127.0.0.1"); err != nil {
		t.Fatalf("回环地址始终允许: %v", err)
	}
	t.Setenv("JUHE_AI_GATEWAY_HEALTH_ALLOW_NON_LOOPBACK", "true")
	for _, host := range []string{"0.0.0.0", "10.42.0.5"} {
		if err := validateHealthListenHost(host); err != nil {
			t.Fatalf("放行开关下私网/未指定地址 %q 必须允许: %v", host, err)
		}
	}
	if err := validateHealthListenHost("192.0.2.10"); err == nil {
		t.Fatal("放行开关下公网地址仍必须拒绝")
	}
	if listener, err := listenLoopback("127.0.0.1:13306"); err != nil {
		t.Fatalf("listenLoopback 回环冒烟: %v", err)
	} else {
		_ = listener.Close()
	}
}

func TestGatewayGoRuntimeCollectorExposesRuntimeKind(t *testing.T) {
	collector := gometrics.New("juhe-ai", "gateway")
	var output strings.Builder
	if err := collector.Write(&output); err != nil {
		t.Fatalf("write metrics: %v", err)
	}
	if !strings.Contains(output.String(), `runtimeKind="go"`) {
		t.Fatal("gateway collector missing runtimeKind label")
	}
}
