package modelcheckowner

import (
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/datadir"
)

// 2026-10-24 存储形态收口：StoreMode 恒 "postgres"（JUHE_AI_J3B_STORE env
// 读取删除），PostgreSQL URL 回退链与自动认领契约保持。

func TestPostgresZeroConfig(t *testing.T) {
	// PG URL 主连接串兜底（零配置语义保留：回退 JUHE_AI_POSTGRES_URL）。
	cfg, err := LoadConfig(func(key string) string {
		if key == "JUHE_AI_POSTGRES_URL" {
			return "postgres://main"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("zero-config J3b must auto-claim: %v", err)
	}
	if !cfg.AutoClaimed || cfg.Owner != "gateway" {
		t.Fatalf("cfg=%+v", cfg)
	}
	if cfg.InstanceID != datadir.DefaultInstanceID() {
		t.Fatalf("instance id default: %q", cfg.InstanceID)
	}
	if !cfg.BusinessHandoffConfirmed || !cfg.NodeWriterStopped || !cfg.SchemaReady || !cfg.HealthBoundaryReady || !cfg.RuntimeReady {
		t.Fatalf("auto-claimed owner flags must be true: %+v", cfg)
	}
	if cfg.OwnerEpoch != "standalone" {
		t.Fatalf("auto-claimed epoch: %+v", cfg)
	}
	if cfg.CredentialSecret != "" || cfg.IdentitySecret != "" {
		t.Fatalf("secrets must defer to composition root fallback: %+v", cfg)
	}
	if cfg.CircuitRuntimeRedisURL != "" {
		t.Fatalf("zero-config mode must allow missing Redis: %+v", cfg)
	}
}

func TestAutoClaimFallsBackToSharedSecret(t *testing.T) {
	values := map[string]string{"JUHE_AI_SECRET": "shared-secret", "JUHE_AI_POSTGRES_URL": "postgres://main"}
	cfg, err := LoadConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if cfg.CredentialSecret != "shared-secret" || cfg.IdentitySecret != "shared-secret" {
		t.Fatalf("cfg=%+v", cfg)
	}
}

func TestAutoClaimExplicitSecretWins(t *testing.T) {
	values := map[string]string{"JUHE_AI_SECRET": "shared", "JUHE_AI_POSTGRES_URL": "postgres://main", "JUHE_AI_J3B_CREDENTIAL_SECRET": "cred", "JUHE_AI_J3B_IDENTITY_SECRET": "identity"}
	cfg, err := LoadConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if cfg.CredentialSecret != "cred" || cfg.IdentitySecret != "identity" {
		t.Fatalf("cfg=%+v", cfg)
	}
}

func TestPostgresStoreAndURLFallbacks(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_POSTGRES_URL":          "postgres://main",
		"JUHE_AI_BUSINESS_POSTGRES_URL": "postgres://business",
	}
	cfg, err := LoadConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if cfg.PostgresURL != "postgres://main" || cfg.BusinessPostgresURL != "postgres://business" {
		t.Fatalf("cfg=%+v", cfg)
	}
}

func TestRejectsNonGatewayOwner(t *testing.T) {
	values := map[string]string{"JUHE_AI_J3B_OWNER": "jobs"}
	if _, err := LoadConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("non-gateway J3b owner must fail closed")
	}
}

// 清理批次 C1（2026-09-30）：owner 事实恒为零配置自动认领，无 Redis 配置时
// 组合根回退进程内 memory 准入（不再有"严格模式缺 Redis fail closed"路径）。
func TestAutoClaimWithoutRedisFallsBackInProcess(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_J3B_OWNER": "gateway", "JUHE_AI_J3B_INSTANCE_ID": "gw-1",
		"JUHE_AI_J3B_POSTGRES_URL": "postgres://j3b", "JUHE_AI_J3B_BUSINESS_POSTGRES_URL": "postgres://business",
		"JUHE_AI_J3B_CREDENTIAL_SECRET": "credential", "JUHE_AI_J3B_IDENTITY_SECRET": "identity",
	}
	cfg, err := LoadConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("auto-claim 无 Redis 不再 fail closed: %v", err)
	}
	if !cfg.AutoClaimed || cfg.CircuitRuntimeRedisURL != "" || cfg.CircuitRuntimeRedisURLFellBack {
		t.Fatalf("auto-claim 无 Redis 应回退 memory 准入: %+v", cfg)
	}
}

// 2026-09-22 namespace canonical 化：J3b 组合根把同一 namespace 喂给
// circuit_runtime（直接拼接型）与 key_model_runtime（双格式去重型），加载层
// 统一剥除 `juhe-ai:` 根前缀；短名逐字节不变；J3B 专属变量优先于共享变量。
func TestCanonicalizesCircuitRuntimeRedisNamespace(t *testing.T) {
	base := map[string]string{
		"JUHE_AI_J3B_OWNER": "gateway", "JUHE_AI_J3B_INSTANCE_ID": "gw-1",
		"JUHE_AI_J3B_POSTGRES_URL": "postgres://j3b", "JUHE_AI_J3B_BUSINESS_POSTGRES_URL": "postgres://business",
		"JUHE_AI_J3B_CREDENTIAL_SECRET": "credential", "JUHE_AI_J3B_IDENTITY_SECRET": "identity", "JUHE_AI_J3B_BUSINESS_HANDOFF_CONFIRMED": "true",
		"JUHE_AI_J3B_NODE_WRITER_STOPPED": "true", "JUHE_AI_J3B_SCHEMA_READY": "true", "JUHE_AI_J3B_HEALTH_BOUNDARY_READY": "true", "JUHE_AI_J3B_RUNTIME_READY": "true",
		"JUHE_AI_J3B_OWNER_EPOCH": "epoch-ns", "JUHE_AI_J3B_CUTOVER_EVIDENCE_PATH": "evidence.json",
		"JUHE_AI_J3B_CIRCUIT_REDIS_URL": "redis://127.0.0.1:6379/9",
	}
	cases := []struct {
		name     string
		j3bNS    string
		sharedNS string
		want     string
	}{
		{name: "全前缀剥根", j3bNS: "juhe-ai:dev", want: "dev"},
		{name: "短名不变", j3bNS: "dev", want: "dev"},
		{name: "空白与全前缀", j3bNS: " juhe-ai:dev ", want: "dev"},
		{name: "专属缺省回落共享全前缀", sharedNS: "juhe-ai:dev", want: "dev"},
	}
	for _, tc := range cases {
		values := map[string]string{}
		for key, value := range base {
			values[key] = value
		}
		values["JUHE_AI_J3B_CIRCUIT_REDIS_NAMESPACE"] = tc.j3bNS
		values["JUHE_AI_REDIS_NAMESPACE"] = tc.sharedNS
		cfg, err := LoadConfig(func(key string) string { return values[key] })
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if cfg.CircuitRuntimeRedisNamespace != tc.want {
			t.Fatalf("%s: namespace=%q want %q", tc.name, cfg.CircuitRuntimeRedisNamespace, tc.want)
		}
	}
}

// TestPostgresURLFallbackChain 钉死 URL 回退链：J3B 专属 URL 缺失回落主
// JUHE_AI_POSTGRES_URL；业务 URL 缺失回落 J3B/主连接串。
func TestPostgresURLFallbackChain(t *testing.T) {
	cfg, err := LoadConfig(func(key string) string {
		if key == "JUHE_AI_POSTGRES_URL" {
			return "postgres://main"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.PostgresURL != "postgres://main" || cfg.BusinessPostgresURL != "postgres://main" {
		t.Fatalf("fallback chain broken: %+v", cfg)
	}
	if _, err := LoadConfig(func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_J3B_POSTGRES_URL") {
		t.Fatalf("全空 env 必须指名缺失的 J3B POSTGRES_URL，got %v", err)
	}
}
