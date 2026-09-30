package modelcheckowner

import (
	"path/filepath"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/datadir"
)

// 2026-09-20 零配置自动认领：handoff/readiness 家族全部未配置时按新装
// standalone 部署处理，sqlite 路径按 datadir 固定名表派生，无 Redis 允许。
func TestAutoClaimsSQLiteZeroConfig(t *testing.T) {
	cfg, err := LoadConfig(func(key string) string {
		if key == "JUHE_AI_J3B_ENABLED" {
			return "true"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("zero-config J3b must auto-claim: %v", err)
	}
	if !cfg.AutoClaimed || cfg.Owner != "gateway" || cfg.StoreMode != "sqlite" {
		t.Fatalf("cfg=%+v", cfg)
	}
	if cfg.InstanceID != datadir.DefaultInstanceID() {
		t.Fatalf("instance id default: %q", cfg.InstanceID)
	}
	if cfg.DatabasePath != filepath.Join(datadir.DefaultDirName, datadir.ModelCheckDatabase) {
		t.Fatalf("j3b database path: %q", cfg.DatabasePath)
	}
	if cfg.BusinessDatabasePath != filepath.Join(datadir.DefaultDirName, datadir.BusinessDatabase) {
		t.Fatalf("business database path: %q", cfg.BusinessDatabasePath)
	}
	if !cfg.BusinessHandoffConfirmed || !cfg.NodeWriterStopped || !cfg.SchemaReady || !cfg.HealthBoundaryReady || !cfg.RuntimeReady {
		t.Fatalf("auto-claimed owner flags must be true: %+v", cfg)
	}
	if cfg.OwnerEpoch != "standalone" || cfg.CutoverEvidencePath != "" {
		t.Fatalf("auto-claimed epoch/evidence: %+v", cfg)
	}
	if cfg.CredentialSecret != "" || cfg.IdentitySecret != "" {
		t.Fatalf("secrets must defer to composition root fallback: %+v", cfg)
	}
	if cfg.CircuitRuntimeRedisURL != "" {
		t.Fatalf("zero-config mode must allow missing Redis: %+v", cfg)
	}
}

func TestAutoClaimFallsBackToSharedSecret(t *testing.T) {
	values := map[string]string{"JUHE_AI_J3B_ENABLED": "true", "JUHE_AI_SECRET": "shared-secret"}
	cfg, err := LoadConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if cfg.CredentialSecret != "shared-secret" || cfg.IdentitySecret != "shared-secret" {
		t.Fatalf("cfg=%+v", cfg)
	}
}

// 业务库路径未显式给 J3b 配置时必须跟随主网关的 Business 库 env。
func TestAutoClaimFollowsMainBusinessDatabasePath(t *testing.T) {
	values := map[string]string{"JUHE_AI_J3B_ENABLED": "true", "JUHE_AI_BUSINESS_DATABASE_PATH": "F:/tmp/main-business.sqlite3"}
	cfg, err := LoadConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if cfg.BusinessDatabasePath != "F:/tmp/main-business.sqlite3" {
		t.Fatalf("business path=%q", cfg.BusinessDatabasePath)
	}
	if cfg.DatabasePath == cfg.BusinessDatabasePath {
		t.Fatalf("j3b path must differ: %q", cfg.DatabasePath)
	}
}

func TestAutoClaimExplicitSecretWins(t *testing.T) {
	values := map[string]string{"JUHE_AI_J3B_ENABLED": "true", "JUHE_AI_SECRET": "shared", "JUHE_AI_J3B_CREDENTIAL_SECRET": "cred", "JUHE_AI_J3B_IDENTITY_SECRET": "identity"}
	cfg, err := LoadConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if cfg.CredentialSecret != "cred" || cfg.IdentitySecret != "identity" {
		t.Fatalf("cfg=%+v", cfg)
	}
}

// 家族内任一成员显式配置即回到严格模式：缺少其余成员必须 fail closed。
func TestPostgresStoreAndURLFallbacks(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_J3B_ENABLED":           "true",
		"JUHE_AI_DATABASE_DRIVER":       "postgres",
		"JUHE_AI_POSTGRES_URL":          "postgres://main",
		"JUHE_AI_BUSINESS_POSTGRES_URL": "postgres://business",
	}
	cfg, err := LoadConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if cfg.StoreMode != "postgres" || cfg.PostgresURL != "postgres://main" || cfg.BusinessPostgresURL != "postgres://business" {
		t.Fatalf("cfg=%+v", cfg)
	}
}

func TestRejectsNonGatewayOwner(t *testing.T) {
	values := map[string]string{"JUHE_AI_J3B_ENABLED": "true", "JUHE_AI_J3B_OWNER": "jobs"}
	if _, err := LoadConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("non-gateway J3b owner must fail closed")
	}
}

// 清理批次 C1（2026-09-30）：owner 事实恒为零配置自动认领，无 Redis 配置时
// 组合根回退进程内 memory 准入（不再有"严格模式缺 Redis fail closed"路径）。
func TestAutoClaimWithoutRedisFallsBackInProcess(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_J3B_ENABLED": "true", "JUHE_AI_J3B_OWNER": "gateway", "JUHE_AI_J3B_INSTANCE_ID": "gw-1",
		"JUHE_AI_J3B_STORE": "postgres", "JUHE_AI_J3B_POSTGRES_URL": "postgres://j3b", "JUHE_AI_J3B_BUSINESS_POSTGRES_URL": "postgres://business",
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
		"JUHE_AI_J3B_ENABLED": "true", "JUHE_AI_J3B_OWNER": "gateway", "JUHE_AI_J3B_INSTANCE_ID": "gw-1",
		"JUHE_AI_J3B_STORE": "postgres", "JUHE_AI_J3B_POSTGRES_URL": "postgres://j3b", "JUHE_AI_J3B_BUSINESS_POSTGRES_URL": "postgres://business",
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

func TestSQLiteReadPoolSizeDefaultsAndValidation(t *testing.T) {
	cfg, err := LoadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatalf("zero-config load: %v", err)
	}
	if cfg.SQLiteReadPoolSize != 4 {
		t.Fatalf("read pool default must be 4, got %d", cfg.SQLiteReadPoolSize)
	}
	cfg, err = LoadConfig(func(key string) string {
		if key == "JUHE_AI_SQLITE_READ_POOL" {
			return "8"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("explicit read pool load: %v", err)
	}
	if cfg.SQLiteReadPoolSize != 8 {
		t.Fatalf("explicit read pool must win, got %d", cfg.SQLiteReadPoolSize)
	}
	for _, invalid := range []string{"0", "17", "abc", "-2"} {
		if _, err := LoadConfig(func(key string) string {
			if key == "JUHE_AI_SQLITE_READ_POOL" {
				return invalid
			}
			return ""
		}); err == nil {
			t.Fatalf("read pool %q must fail closed", invalid)
		}
	}
}
